package jsvm

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	validation "ganigeorgiev.com/pocketbase/ozzo-validation/v4"
	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/forms"
	"ganigeorgiev.com/pocketbase/pocketbase/mails"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/filesystem"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/hook"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/inflector"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/mailer"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/router"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/store"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/subscriptions"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/cast"
	"github.com/spf13/cobra"
)

// The binds mirror PocketBase's plugins/jsvm binds.go: same globals, same names, same behavior, on this engine.
// Each Bind* must run on the loop (inside vm.Do or setup).

// call is one call of a native function.
type call struct {
	vm   *VM
	this JSValue
	args []JSValue
	ctor bool
}

func (c *call) len() int { return len(c.args) }

// export converts argument i to Go (nil when missing), the way goja's Export does.
func (c *call) export(i int) any {
	if i >= len(c.args) {
		return nil
	}
	v, err := c.vm.fromJS(c.args[i], nil)
	if err != nil || !v.IsValid() {
		return nil
	}
	return v.Interface()
}

// into converts argument i to t (its zero value when missing).
func (c *call) into(i int, t reflect.Type) (reflect.Value, error) {
	if i >= len(c.args) {
		return reflect.Zero(t), nil
	}
	return c.vm.fromJS(c.args[i], t)
}

func (c *call) str(i int) string {
	if i >= len(c.args) || C.avm_tag(c.args[i]) == C.JS_TAG_UNDEFINED {
		return ""
	}
	return c.vm.goString(c.args[i])
}

func (c *call) isFunc(i int) bool {
	return i < len(c.args) && C.avm_handle_of(c.vm.a, c.args[i]) == 0 && C.avm_is_function(c.vm.ctx, c.args[i]) != 0
}

// goValue is the Go value behind argument i if it's a Go object or function.
func (c *call) goValue(i int) (reflect.Value, bool) {
	if i >= len(c.args) {
		return reflect.Value{}, false
	}
	h := C.avm_handle_of(c.vm.a, c.args[i])
	if h == 0 || c.vm.handles[h].kind == kindNative {
		return reflect.Value{}, false
	}
	return c.vm.handles[h].v, true
}

type nativeGo func(c *call) (any, error)

func (vm *VM) wrapNative(fn nativeGo) nativeFn {
	return func(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
		res, err := fn(&call{vm: vm, this: this, args: args, ctor: ctor})
		if err != nil {
			return vm.throw(err)
		}
		if jv, ok := res.(JSValue); ok {
			return jv
		}
		jv, err := vm.toJS(reflect.ValueOf(res))
		if err != nil {
			return vm.throw(err)
		}
		return jv
	}
}

// nativeValue makes a JS function (optionally a constructor) from fn.
func (vm *VM) nativeValue(fn nativeGo, ctor bool) JSValue {
	h, _ := vm.newHandle(entry{kind: kindNative, native: vm.wrapNative(fn)})
	ctorBit := 0
	if ctor {
		ctorBit = 1
	}
	return C.avm_new_func(vm.a, uint32(h), int32(ctorBit))
}

// onSetGo, when set (tests only), sees every Go value given to JS as a global: the shared-map audit walks them.
var onSetGo func(name string, v any)

// setGo sets a global to a Go value, enumerable like goja's vm.Set.
func (vm *VM) setGo(name string, v any) {
	if onSetGo != nil {
		onSetGo(name, v)
	}
	jv, err := vm.toJS(reflect.ValueOf(v))
	if err != nil {
		panic(err)
	}
	vm.setGlobal(name, jv)
}

func (vm *VM) setFunc(name string, fn nativeGo) { vm.setGlobal(name, vm.nativeValue(fn, false)) }

// setCtor sets a global Go constructor with a real `prototype` (inheriting the Go objects' prototype), so
// `x instanceof Name` works and `new Name()` instances are instances of it, as in goja.
func (vm *VM) setCtor(name string, fn nativeGo) {
	f := vm.nativeValue(fn, true)
	C.avm_ctor_proto(vm.ctx, f, vm.objProto)
	vm.setGlobal(name, f)
}

// setHidden sets a non-enumerable global (engine internals, not counted as binds).
func (vm *VM) setHidden(name string, v JSValue) {
	g := C.JS_GetGlobalObject(vm.ctx)
	cs := C.CString(name)
	C.avm_define_hidden(vm.ctx, g, cs, v)
	C.free(cs)
	vm.free_(g)
}

// namespace exposes a Go map of functions and values as a global object ($dbx, $security, ...).
func (vm *VM) namespace(name string, members map[string]any) { vm.setGo(name, members) }

// loadInto sets each property of a plain JS object argument on a Go object (goja's structConstructor).
func (vm *VM) loadInto(target JSValue, src JSValue) {
	if C.avm_tag(src) != C.JS_TAG_OBJECT || C.avm_handle_of(vm.a, src) != 0 {
		return
	}
	keys := C.avm_object_keys(vm.ctx, src)
	if C.avm_is_exception(keys) != 0 {
		vm.free_(C.JS_GetException(vm.ctx))
		return
	}
	defer vm.free_(keys)
	n := int(C.avm_length(vm.ctx, keys))
	for i := 0; i < n; i++ {
		kv := C.avm_array_get(vm.ctx, keys, uint32(i))
		k := vm.goString(kv)
		vm.free_(kv)
		ck := C.CString(k)
		val := C.avm_get_str(vm.ctx, src, ck)
		C.JS_SetPropertyStr(vm.ctx, target, ck, val) // takes val
		C.free(ck)
		if C.JS_HasException(vm.ctx) {
			vm.free_(C.JS_GetException(vm.ctx))
		}
	}
}

func (vm *VM) structCtor(newInstance func() any) nativeGo {
	return func(c *call) (any, error) {
		jv, err := vm.toJS(reflect.ValueOf(newInstance()))
		if err != nil {
			return nil, err
		}
		if c.len() > 0 {
			vm.loadInto(jv, c.args[0])
		}
		return jv, nil
	}
}

func (vm *VM) structCtorUnmarshal(newInstance func() any) nativeGo {
	return func(c *call) (any, error) {
		instance := newInstance()
		if data := c.export(0); data != nil {
			if raw, err := json.Marshal(data); err == nil {
				_ = json.Unmarshal(raw, instance)
			}
		}
		return instance, nil
	}
}

// registerFactoryAsConstructor exposes a Go factory as a JS constructor; missing arguments are zero values.
func (vm *VM) registerFactoryAsConstructor(name string, factory any) {
	rv := reflect.ValueOf(factory)
	vm.setCtor(name, func(c *call) (any, error) {
		in, err := vm.goArgs(rv.Type(), c.args)
		if err != nil {
			return nil, err
		}
		out := rv.Call(in)
		if len(out) != 1 {
			return nil, errors.New("the factory function should return only 1 item")
		}
		return out[0].Interface(), nil
	})
}

// -------------------------------------------------------------------

var cachedArrayOfTypes = store.New[reflect.Type, reflect.Type](nil)

// BindCore registers common core objects and functions such as sleep, toString, DynamicModel, etc.
func BindCore(vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { BindCore(vm) })
		return
	}
	// deprecated: use toString
	vm.setGo("readerToString", func(r io.Reader, maxBytes int) (string, error) {
		if maxBytes == 0 {
			maxBytes = router.DefaultMaxMemory
		}
		bodyBytes, readErr := io.ReadAll(io.LimitReader(r, int64(maxBytes)))
		if readErr != nil {
			return "", readErr
		}
		return string(bodyBytes), nil
	})

	// note: throw only on reader error
	vm.setGo("toBytes", func(raw any, maxReaderBytes int) ([]byte, error) {
		switch v := raw.(type) {
		case nil:
			return []byte{}, nil
		case string:
			return []byte(v), nil
		case []byte:
			return v, nil
		case types.JSONRaw:
			return v, nil
		case io.Reader:
			if maxReaderBytes == 0 {
				maxReaderBytes = router.DefaultMaxMemory
			}
			return io.ReadAll(io.LimitReader(v, int64(maxReaderBytes)))
		default:
			if b, err := cast.ToUint8SliceE(v); err == nil {
				return b, nil
			}
			if str, err := cast.ToStringE(v); err == nil {
				return []byte(str), nil
			}
			rawBytes, _ := json.Marshal(raw, json.Deterministic(true))
			return rawBytes, nil
		}
	})

	// note: throw only on reader error
	vm.setGo("toString", func(raw any, maxReaderBytes int) (string, error) {
		switch v := raw.(type) {
		case io.Reader:
			if maxReaderBytes == 0 {
				maxReaderBytes = router.DefaultMaxMemory
			}
			bodyBytes, readErr := io.ReadAll(io.LimitReader(v, int64(maxReaderBytes)))
			if readErr != nil {
				return "", readErr
			}
			return string(bodyBytes), nil
		default:
			if str, err := cast.ToStringE(v); err == nil {
				return str, nil
			}
			rawBytes, _ := json.Marshal(raw, json.Deterministic(true))
			return string(rawBytes), nil
		}
	})

	maxSleep := vm.cfg.MaxSleep
	sh, _ := vm.newHandle(entry{v: reflect.ValueOf(func(milliseconds int64) {
		d := time.Duration(milliseconds) * time.Millisecond
		if maxSleep > 0 && d > maxSleep {
			d = maxSleep
		}
		time.Sleep(d)
	}), kind: kindFn, bytes: handleBase, blocking: true})
	vm.setGlobal("sleep", C.avm_new_func(vm.a, uint32(sh), 0))

	vm.setFunc("arrayOf", func(c *call) (any, error) {
		mv, ok := c.goValue(0)
		if !ok {
			v := c.export(0)
			if v == nil {
				return nil, errors.New("arrayOf expects a model")
			}
			mv = reflect.ValueOf(v)
		}
		st := cachedArrayOfTypes.GetOrSet(mv.Type(), func() reflect.Type { return reflect.SliceOf(mv.Type()) })
		return reflect.New(st).Elem().Addr().Interface(), nil
	})

	vm.setGo("unmarshal", func(data, dst any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &dst)
	})

	vm.setCtor("Context", func(c *call) (any, error) {
		instance, ok := c.export(0).(context.Context)
		if !ok {
			instance = context.Background()
		}
		if key := c.export(1); key != nil {
			instance = context.WithValue(instance, key, c.export(2))
		}
		return instance, nil
	})

	vm.setCtor("DynamicModel", func(c *call) (any, error) {
		sv, err := c.into(0, reflect.TypeOf(map[string]any{}))
		if err != nil {
			return nil, err
		}
		shape, _ := sv.Interface().(map[string]any)
		if len(shape) == 0 {
			return nil, errors.New("[DynamicModel] missing shape data")
		}
		return newDynamicModel(shape), nil
	})

	// nullable helpers usually used as DynamicModel shape values
	vm.setGo("nullString", func() *string { var v string; return &v })
	vm.setGo("nullFloat", func() *float64 { var v float64; return &v })
	vm.setGo("nullInt", func() *int64 { var v int64; return &v })
	vm.setGo("nullBool", func() *bool { var v bool; return &v })
	vm.setGo("nullArray", func() *types.JSONArray[any] { var v types.JSONArray[any]; return &v })
	vm.setGo("nullObject", func() *types.JSONMap[any] { var v types.JSONMap[any]; return &v })

	vm.setCtor("Record", func(c *call) (any, error) {
		cv, _ := c.goValue(0)
		if col, ok := goIface[*core.Collection](cv); ok && col != nil {
			instance := core.NewRecord(col)
			if c.len() > 1 {
				if dv, err := c.into(1, reflect.TypeOf(map[string]any{})); err == nil {
					if data, ok := dv.Interface().(map[string]any); ok {
						instance.Load(data)
					}
				}
			}
			return instance, nil
		}
		return &core.Record{}, nil
	})

	vm.setCtor("Collection", vm.structCtorUnmarshal(func() any { return &core.Collection{} }))
	vm.setCtor("FieldsList", vm.structCtorUnmarshal(func() any { return &core.FieldsList{} }))

	// fields
	vm.setCtor("Field", func(c *call) (any, error) {
		data, _ := c.export(0).(map[string]any)
		rawDataSlice, _ := json.Marshal([]any{data})
		fieldsList := core.NewFieldsList()
		_ = fieldsList.UnmarshalJSON(rawDataSlice)
		if len(fieldsList) == 0 {
			return nil, nil
		}
		return fieldsList[0], nil
	})
	vm.setCtor("NumberField", vm.structCtorUnmarshal(func() any { return &core.NumberField{} }))
	vm.setCtor("BoolField", vm.structCtorUnmarshal(func() any { return &core.BoolField{} }))
	vm.setCtor("TextField", vm.structCtorUnmarshal(func() any { return &core.TextField{} }))
	vm.setCtor("URLField", vm.structCtorUnmarshal(func() any { return &core.URLField{} }))
	vm.setCtor("EmailField", vm.structCtorUnmarshal(func() any { return &core.EmailField{} }))
	vm.setCtor("EditorField", vm.structCtorUnmarshal(func() any { return &core.EditorField{} }))
	vm.setCtor("PasswordField", vm.structCtorUnmarshal(func() any { return &core.PasswordField{} }))
	vm.setCtor("DateField", vm.structCtorUnmarshal(func() any { return &core.DateField{} }))
	vm.setCtor("AutodateField", vm.structCtorUnmarshal(func() any { return &core.AutodateField{} }))
	vm.setCtor("JSONField", vm.structCtorUnmarshal(func() any { return &core.JSONField{} }))
	vm.setCtor("RelationField", vm.structCtorUnmarshal(func() any { return &core.RelationField{} }))
	vm.setCtor("SelectField", vm.structCtorUnmarshal(func() any { return &core.SelectField{} }))
	vm.setCtor("FileField", vm.structCtorUnmarshal(func() any { return &core.FileField{} }))
	vm.setCtor("GeoPointField", vm.structCtorUnmarshal(func() any { return &core.GeoPointField{} }))

	vm.setCtor("MailerMessage", vm.structCtor(func() any { return &mailer.Message{} }))
	vm.setCtor("Command", vm.structCtor(func() any { return &cobra.Command{} }))
	vm.setCtor("RequestInfo", vm.structCtor(func() any { return &core.RequestInfo{Context: core.RequestInfoContextDefault} }))

	// new Middleware((e) => e.next(), 100, "example_middleware")
	vm.setCtor("Middleware", func(c *call) (any, error) {
		m := &jsMiddleware{priority: cast.ToInt(c.export(1)), id: cast.ToString(c.export(2))}
		switch {
		case c.isFunc(0):
			m.fn = vm.ref(c.args[0])
		default:
			if gv, ok := c.goValue(0); ok {
				if f, ok := gv.Interface().(func(*core.RequestEvent) error); ok {
					m.native = f
				}
			}
		}
		return m, nil
	})

	// note: named Timezone to avoid conflicts with the JS Location interface.
	vm.setCtor("Timezone", func(c *call) (any, error) {
		instance, err := time.LoadLocation(c.str(0))
		if err != nil {
			instance = time.UTC
		}
		return instance, nil
	})

	vm.setCtor("DateTime", func(c *call) (any, error) {
		instance := types.NowDateTime()
		rawDate, _ := c.export(0).(string)
		locName, _ := c.export(1).(string)
		if rawDate != "" && locName != "" {
			loc, err := time.LoadLocation(locName)
			if err != nil {
				loc = time.UTC
			}
			instance, _ = types.ParseDateTime(cast.ToTimeInDefaultLocation(rawDate, loc))
		} else if rawDate != "" {
			instance, _ = types.ParseDateTime(rawDate)
		}
		return &instance, nil
	})

	vm.setCtor("ValidationError", func(c *call) (any, error) {
		code, _ := c.export(0).(string)
		message, _ := c.export(1).(string)
		return validation.NewError(code, message), nil
	})

	vm.setCtor("Cookie", vm.structCtor(func() any { return &http.Cookie{} }))
	vm.setCtor("SubscriptionMessage", vm.structCtor(func() any { return &subscriptions.Message{} }))
}

// goIface unwraps v (a pointer or value) as T.
func goIface[T any](v reflect.Value) (T, bool) {
	var zero T
	if !v.IsValid() {
		return zero, false
	}
	if t, ok := v.Interface().(T); ok {
		return t, true
	}
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		if t, ok := v.Elem().Interface().(T); ok {
			return t, true
		}
	}
	return zero, false
}

// BindDbx registers $dbx.* with the dbx query expression helpers.
func BindDbx(vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { BindDbx(vm) })
		return
	}
	vm.namespace("$dbx", map[string]any{
		"exp":        dbx.NewExp,
		"hashExp":    func(data map[string]any) dbx.HashExp { return dbx.HashExp(data) },
		"not":        dbx.Not,
		"and":        dbx.And,
		"or":         dbx.Or,
		"in":         dbx.In,
		"notIn":      dbx.NotIn,
		"like":       dbx.Like,
		"orLike":     dbx.OrLike,
		"notLike":    dbx.NotLike,
		"orNotLike":  dbx.OrNotLike,
		"exists":     dbx.Exists,
		"notExists":  dbx.NotExists,
		"between":    dbx.Between,
		"notBetween": dbx.NotBetween,
	})
}

// BindMails registers $mails.* with the record mail helpers.
func BindMails(vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { BindMails(vm) })
		return
	}
	vm.namespace("$mails", map[string]any{
		"sendRecordPasswordReset": mails.SendRecordPasswordReset,
		"sendRecordVerification":  mails.SendRecordVerification,
		"sendRecordChangeEmail":   mails.SendRecordChangeEmail,
		"sendRecordOTP":           mails.SendRecordOTP,
		"sendRecordAuthAlert":     mails.SendRecordAuthAlert,
	})
}

// BindSecurity registers $security.* with crypto, random, JWT and encryption helpers.
func BindSecurity(vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { BindSecurity(vm) })
		return
	}
	vm.namespace("$security", map[string]any{
		"md5":                            security.MD5,
		"sha256":                         security.SHA256,
		"sha512":                         security.SHA512,
		"hs256":                          security.HS256,
		"hs512":                          security.HS512,
		"equal":                          security.Equal,
		"randomString":                   security.RandomString,
		"randomStringByRegex":            security.RandomStringByRegex,
		"randomStringWithAlphabet":       security.RandomStringWithAlphabet,
		"pseudorandomString":             security.PseudorandomString,
		"pseudorandomStringWithAlphabet": security.PseudorandomStringWithAlphabet,
		"parseUnverifiedJWT": func(token string) (map[string]any, error) {
			return security.ParseUnverifiedJWT(token)
		},
		"parseJWT": func(token string, verificationKey string) (map[string]any, error) {
			return security.ParseJWT(token, verificationKey)
		},
		"createJWT": func(payload jwt.MapClaims, signingKey string, secDuration int) (string, error) {
			return security.NewJWT(payload, signingKey, time.Duration(secDuration)*time.Second)
		},
		"encrypt": security.Encrypt,
		"decrypt": func(cipherText, key string) (string, error) {
			result, err := security.Decrypt(cipherText, key)
			if err != nil {
				return "", err
			}
			return string(result), err
		},
	})
}

// BindFilesystem registers $filesystem.* with the filesystem package helpers.
func BindFilesystem(vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { BindFilesystem(vm) })
		return
	}
	vm.namespace("$filesystem", map[string]any{
		"s3":                filesystem.NewS3,
		"local":             filesystem.NewLocal,
		"fileFromPath":      filesystem.NewFileFromPath,
		"fileFromBytes":     filesystem.NewFileFromBytes,
		"fileFromMultipart": filesystem.NewFileFromMultipart,
		"fileFromURL": func(url string, secTimeout int) (*filesystem.File, error) {
			if secTimeout == 0 {
				secTimeout = 120
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secTimeout)*time.Second)
			defer cancel()
			return filesystem.NewFileFromURL(ctx, url)
		},
	})
}

// BindFilepath registers $filepath.* with Go's path/filepath.
func BindFilepath(vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { BindFilepath(vm) })
		return
	}
	vm.namespace("$filepath", map[string]any{
		"base":      filepath.Base,
		"clean":     filepath.Clean,
		"dir":       filepath.Dir,
		"ext":       filepath.Ext,
		"fromSlash": filepath.FromSlash,
		"glob":      filepath.Glob,
		"isAbs":     filepath.IsAbs,
		"join":      filepath.Join,
		"match":     filepath.Match,
		"rel":       filepath.Rel,
		"split":     filepath.Split,
		"splitList": filepath.SplitList,
		"toSlash":   filepath.ToSlash,
		"walk":      filepath.Walk,
		"walkDir":   filepath.WalkDir,
	})
}

// BindOS registers $os.* with Go's os package.
func BindOS(vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { BindOS(vm) })
		return
	}
	vm.namespace("$os", map[string]any{
		"args":       os.Args,
		"exec":       exec.Command, // @deprecated
		"cmd":        exec.Command,
		"exit":       os.Exit,
		"getenv":     os.Getenv,
		"dirFS":      dirFS,
		"stat":       os.Stat,
		"readFile":   os.ReadFile,
		"writeFile":  os.WriteFile,
		"readDir":    os.ReadDir,
		"tempDir":    os.TempDir,
		"truncate":   os.Truncate,
		"getwd":      os.Getwd,
		"mkdir":      os.Mkdir,
		"mkdirAll":   os.MkdirAll,
		"rename":     os.Rename,
		"remove":     os.Remove,
		"removeAll":  os.RemoveAll,
		"openRoot":   os.OpenRoot,
		"openInRoot": os.OpenInRoot,
	})
}

// BindForms registers the app form constructors (kept for backward compatibility).
func BindForms(vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { BindForms(vm) })
		return
	}
	vm.registerFactoryAsConstructor("AppleClientSecretCreateForm", forms.NewAppleClientSecretCreate)
	vm.registerFactoryAsConstructor("RecordUpsertForm", forms.NewRecordUpsert)
	vm.registerFactoryAsConstructor("TestEmailSendForm", forms.NewTestEmailSend)
	vm.registerFactoryAsConstructor("TestS3FilesystemForm", forms.NewTestS3Filesystem)
}

// BindApis registers $apis.* (handlers, middlewares, record helpers) and the API error constructors.
func BindApis(vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { BindApis(vm) })
		return
	}
	vm.namespace("$apis", map[string]any{
		"static": func(dirOrFS any, indexFallback bool) (func(*core.RequestEvent) error, error) {
			switch v := dirOrFS.(type) {
			case fs.FS:
				return apis.Static(v, indexFallback), nil
			case string:
				return apis.Static(os.DirFS(v), indexFallback), nil
			default:
				return nil, errors.New("$apis.static expects the first argument to be either a plain string path or fs.FS value")
			}
		},
		"requireGuestOnly":            apis.RequireGuestOnly,
		"requireAuth":                 apis.RequireAuth,
		"requireSuperuserAuth":        apis.RequireSuperuserAuth,
		"requireSuperuserOrOwnerAuth": apis.RequireSuperuserOrOwnerAuth,
		"skipSuccessActivityLog":      apis.SkipSuccessActivityLog,
		"gzip":                        apis.Gzip,
		"bodyLimit":                   apis.BodyLimit,
		"recordAuthResponse":          apis.RecordAuthResponse,
		"enrichRecord":                apis.EnrichRecord,
		"enrichRecords":               apis.EnrichRecords,
	})

	vm.registerFactoryAsConstructor("ApiError", router.NewApiError)
	vm.registerFactoryAsConstructor("NotFoundError", router.NewNotFoundError)
	vm.registerFactoryAsConstructor("BadRequestError", router.NewBadRequestError)
	vm.registerFactoryAsConstructor("ForbiddenError", router.NewForbiddenError)
	vm.registerFactoryAsConstructor("UnauthorizedError", router.NewUnauthorizedError)
	vm.registerFactoryAsConstructor("TooManyRequestsError", router.NewTooManyRequestsError)
	vm.registerFactoryAsConstructor("InternalServerError", router.NewInternalServerError)
}

type sendResult struct {
	JSON    any                     `json:"json"`
	Headers map[string][]string     `json:"headers"`
	Cookies map[string]*http.Cookie `json:"cookies"`

	// Deprecated: consider using Body instead
	Raw string `json:"raw"`

	Body       []byte `json:"body"`
	StatusCode int    `json:"statusCode"`
}

// BindHTTP registers $http.send and the FormData constructor. Requests go through the VM's HTTP client (an
// extension's HTTPClient, else http.DefaultClient).
func BindHTTP(vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { BindHTTP(vm) })
		return
	}
	vm.setCtor("FormData", func(c *call) (any, error) { return FormData{}, nil })

	type sendConfig struct {
		Data    map[string]any // Deprecated: consider using Body instead
		Body    any            // raw string or FormData
		Headers map[string]string
		Method  string
		Url     string
		Timeout int // seconds (default to 120)
	}

	send := func(params map[string]any) (*sendResult, error) {
		config := sendConfig{Method: "GET"}
		if v, ok := params["data"]; ok {
			config.Data = cast.ToStringMap(v)
		}
		if v, ok := params["body"]; ok {
			config.Body = v
		}
		if v, ok := params["headers"]; ok {
			config.Headers = cast.ToStringMapString(v)
		}
		if v, ok := params["method"]; ok {
			config.Method = cast.ToString(v)
		}
		if v, ok := params["url"]; ok {
			config.Url = cast.ToString(v)
		}
		if v, ok := params["timeout"]; ok {
			config.Timeout = cast.ToInt(v)
		}
		if config.Timeout <= 0 {
			config.Timeout = 120
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.Timeout)*time.Second)
		defer cancel()

		var reqBody io.Reader
		var contentType string
		if len(config.Data) != 0 {
			encoded, err := json.Marshal(config.Data)
			if err != nil {
				return nil, err
			}
			reqBody = bytes.NewReader(encoded)
		} else {
			switch v := config.Body.(type) {
			case io.Reader:
				reqBody = v
			case FormData:
				body, mp, err := v.toMultipart()
				if err != nil {
					return nil, err
				}
				reqBody = body
				contentType = mp.FormDataContentType()
			case *FormData:
				body, mp, err := v.toMultipart()
				if err != nil {
					return nil, err
				}
				reqBody = body
				contentType = mp.FormDataContentType()
			default:
				reqBody = strings.NewReader(cast.ToString(config.Body))
			}
		}

		req, err := http.NewRequestWithContext(ctx, strings.ToUpper(config.Method), config.Url, reqBody)
		if err != nil {
			return nil, err
		}
		for k, v := range config.Headers {
			req.Header.Add(k, v)
		}
		if contentType != "" {
			req.Header.Set("content-type", contentType)
		}

		res, err := vm.httpClient().Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()

		bodyRaw, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, err
		}
		result := &sendResult{
			StatusCode: res.StatusCode,
			Headers:    map[string][]string{},
			Cookies:    map[string]*http.Cookie{},
			Raw:        string(bodyRaw),
			Body:       bodyRaw,
		}
		for k, v := range res.Header {
			result.Headers[k] = v
		}
		for _, v := range res.Cookies() {
			result.Cookies[v.Name] = v
		}
		if len(result.Body) > 0 {
			result.JSON = map[string]any{}
			if err := json.Unmarshal(bodyRaw, &result.JSON); err != nil {
				result.JSON = []any{}
				if err := json.Unmarshal(bodyRaw, &result.JSON); err != nil {
					result.JSON = nil
				}
			}
		}
		return result, nil
	}

	sendH, _ := vm.newHandle(entry{v: reflect.ValueOf(send), kind: kindFn, bytes: handleBase, blocking: true})
	ns := map[string]any{}
	nsV, _ := vm.toJS(reflect.ValueOf(ns))
	cs := C.CString("send")
	C.JS_SetPropertyStr(vm.ctx, nsV, cs, C.avm_new_func(vm.a, uint32(sendH), 0))
	C.free(cs)
	vm.setGlobal("$http", nsV)
}

func (vm *VM) httpClient() *http.Client {
	if vm.HTTPClient != nil {
		return vm.HTTPClient
	}
	return http.DefaultClient
}

// -------------------------------------------------------------------

// jsMiddleware is what `new Middleware(fn, priority, id)` returns.
type jsMiddleware struct {
	fn       *jsRef
	native   func(*core.RequestEvent) error
	priority int
	id       string
}

// goFS keeps a file system a Go value in JS. os.DirFS's own type is a named string, which reaches JS as a plain
// string, so { fs: $os.dirFS(dir) } lost it (a superuser UI extension's fs, pockethost.io/docs/admin-extensions).
type goFS struct{ fs.FS }

// dirFS is $os.dirFS: os.DirFS(dir), kept a Go value; it still answers ReadFile and Stat like os.DirFS.
func dirFS(dir string) fs.FS { return goFS{os.DirFS(dir)} }

func (f goFS) ReadFile(name string) ([]byte, error)  { return fs.ReadFile(f.FS, name) }
func (f goFS) Stat(name string) (fs.FileInfo, error) { return fs.Stat(f.FS, name) }

// hooksBinds adds onX(handler, ...tags) for every app.OnX hook (except OnServe), by reflecting on core.App.
func hooksBinds(app core.App, vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { hooksBinds(app, vm) })
		return
	}
	appV := reflect.ValueOf(app)
	appT := appV.Type()
	for i := 0; i < appT.NumMethod(); i++ {
		m := appT.Method(i)
		if !strings.HasPrefix(m.Name, "On") || m.Name == "OnServe" {
			continue
		}
		name := m.Name
		vm.setFunc(jsName(name), func(c *call) (any, error) {
			if !c.isFunc(0) {
				return nil, errors.New(jsName(name) + "(handler, ...tags) expects a function")
			}
			h := vm.ref(c.args[0])
			var tags []reflect.Value
			for i := 1; i < c.len(); i++ {
				tags = append(tags, reflect.ValueOf(c.str(i)))
			}
			hookV := appV.MethodByName(name).Call(tags)[0]
			bind := hookV.MethodByName("BindFunc")
			ht := bind.Type().In(0)
			fn := reflect.MakeFunc(ht, func(in []reflect.Value) []reflect.Value {
				ev := in[0].Interface()
				err := vm.runHandler(h, eventApp(app, in[0]), ev)
				return []reflect.Value{reflect.ValueOf(&err).Elem()}
			})
			bind.Call([]reflect.Value{fn})
			return nil, nil
		})
	}
}

// eventApp is the app of a hook event (the transaction's app inside a transaction), else app.
func eventApp(app core.App, ev reflect.Value) core.App {
	if f := reflect.Indirect(ev).FieldByName("App"); f.IsValid() {
		if a, ok := f.Interface().(core.App); ok && a != nil {
			return a
		}
	}
	return app
}

// cronBinds adds cronAdd(id, expr, handler) and cronRemove(id).
func cronBinds(app core.App, vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { cronBinds(app, vm) })
		return
	}
	vm.setFunc("cronAdd", func(c *call) (any, error) {
		jobId, cronExpr := c.str(0), c.str(1)
		if !c.isFunc(2) {
			return nil, errors.New("[cronAdd] the handler must be a function")
		}
		h := vm.ref(c.args[2])
		err := app.Cron().Add(jobId, cronExpr, func() {
			if err := vm.runHandler(h, app); err != nil {
				app.Logger().Error("[cronAdd] failed to execute cron job", slog.String("jobId", jobId), slog.String("error", err.Error()))
			}
		})
		if err != nil {
			return nil, fmt.Errorf("[cronAdd] failed to register cron job %s: %w", jobId, err)
		}
		return nil, nil
	})
	vm.setGo("cronRemove", func(jobId string) { app.Cron().Remove(jobId) })
}

// routerBinds adds routerAdd(method, path, handler, ...middlewares) and routerUse(...middlewares).
func routerBinds(app core.App, vm *VM) {
	if !vm.onLoop() {
		_ = vm.Do(func() { routerBinds(app, vm) })
		return
	}
	vm.setFunc("routerAdd", func(c *call) (any, error) {
		method, path := strings.ToUpper(c.str(0)), c.str(1)
		handler, err := vm.wrapHandler(c, 2)
		if err != nil {
			return nil, fmt.Errorf("[routerAdd] failed to wrap handler: %w", err)
		}
		mws, err := vm.wrapMiddlewares(c, 3)
		if err != nil {
			return nil, fmt.Errorf("[routerAdd] failed to wrap middlewares: %w", err)
		}
		app.OnServe().BindFunc(func(e *core.ServeEvent) error {
			e.Router.Route(method, path, handler).Bind(mws...)
			return e.Next()
		})
		return nil, nil
	})
	vm.setFunc("routerUse", func(c *call) (any, error) {
		mws, err := vm.wrapMiddlewares(c, 0)
		if err != nil {
			return nil, fmt.Errorf("[routerUse] failed to wrap middlewares: %w", err)
		}
		app.OnServe().BindFunc(func(e *core.ServeEvent) error {
			e.Router.Bind(mws...)
			return e.Next()
		})
		return nil, nil
	})
}

func (vm *VM) wrapHandler(c *call, i int) (func(*core.RequestEvent) error, error) {
	if c.isFunc(i) {
		h := vm.ref(c.args[i])
		return func(e *core.RequestEvent) error { return vm.runHandler(h, e.App, e) }, nil
	}
	if gv, ok := c.goValue(i); ok {
		if f, ok := gv.Interface().(func(*core.RequestEvent) error); ok {
			return f, nil
		}
	}
	return nil, errors.New("handler must be a function")
}

func (vm *VM) wrapMiddlewares(c *call, from int) ([]*hook.Handler[*core.RequestEvent], error) {
	var out []*hook.Handler[*core.RequestEvent]
	for i := from; i < c.len(); i++ {
		if c.isFunc(i) {
			h := vm.ref(c.args[i])
			out = append(out, &hook.Handler[*core.RequestEvent]{Func: func(e *core.RequestEvent) error { return vm.runHandler(h, e.App, e) }})
			continue
		}
		gv, ok := c.goValue(i)
		if !ok {
			return nil, errors.New("middleware must be a function, a Middleware or a Go handler")
		}
		switch v := gv.Interface().(type) {
		case *hook.Handler[*core.RequestEvent]:
			out = append(out, v)
		case func(*core.RequestEvent) error:
			out = append(out, &hook.Handler[*core.RequestEvent]{Func: v})
		case *jsMiddleware:
			f := v.native
			if v.fn != nil {
				h := v.fn
				f = func(e *core.RequestEvent) error { return vm.runHandler(h, e.App, e) }
			}
			if f == nil {
				return nil, errors.New("missing or invalid Middleware function")
			}
			out = append(out, &hook.Handler[*core.RequestEvent]{Id: v.id, Priority: v.priority, Func: f})
		default:
			return nil, fmt.Errorf("unsupported middleware type %T", v)
		}
	}
	return out, nil
}

// -------------------------------------------------------------------

var cachedDynamicModelStructs = store.New[string, reflect.Type](nil)

// newDynamicModel creates a struct with fields from the "shape" (values are the defaults); see plugins/jsvm.
func newDynamicModel(shape map[string]any) any {
	info := make([]*shapeFieldInfo, 0, len(shape))
	var hash strings.Builder
	sortedKeys := make([]string, 0, len(shape))
	for k := range shape {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	for _, k := range sortedKeys {
		v := shape[k]
		vt := reflect.TypeOf(v)
		if vt == nil {
			vt = reflect.TypeOf((*any)(nil)).Elem()
		}
		switch vt.Kind() {
		case reflect.Map:
			raw, _ := json.Marshal(v)
			newV := types.JSONMap[any]{}
			_ = newV.Scan(raw)
			v = newV
			vt = reflect.TypeOf(v)
		case reflect.Slice, reflect.Array:
			raw, _ := json.Marshal(v)
			newV := types.JSONArray[any]{}
			_ = newV.Scan(raw)
			v = newV
			vt = reflect.TypeOf(newV)
		case reflect.Pointer:
			v = nil
		}
		hash.WriteString(k)
		hash.WriteString(":")
		hash.WriteString(vt.String())
		hash.WriteString("|")
		info = append(info, &shapeFieldInfo{key: k, value: v, valueType: vt})
	}

	st := cachedDynamicModelStructs.GetOrSet(hash.String(), func() reflect.Type {
		structFields := make([]reflect.StructField, len(info))
		for i, item := range info {
			structFields[i] = reflect.StructField{
				Name: inflector.UcFirst(item.key),
				Type: item.valueType,
				Tag:  reflect.StructTag(`db:"` + item.key + `" json:"` + item.key + `" form:"` + item.key + `"`),
			}
		}
		return reflect.StructOf(structFields)
	})

	elem := reflect.New(st).Elem()
	for i, item := range info {
		if item.value == nil {
			continue
		}
		elem.Field(i).Set(reflect.ValueOf(item.value))
	}
	return elem.Addr().Interface()
}

type shapeFieldInfo struct {
	value     any
	valueType reflect.Type
	key       string
}

var _ = slices.Contains[[]string]
