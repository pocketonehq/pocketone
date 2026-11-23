package standard

import (
	"embed"
	"encoding/json"
	"io/fs"
	"strings"
	"testing/fstest"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/hook"
	"pocketone.dev/ext/pocker"
)

//go:embed branding
var brandingFiles embed.FS

// brandingName is the dashboard extension's name: its files are served at /_/extensions/pocketone/.
const brandingName = "pocketone"

// bindBranding gives app's superuser dashboard PocketOne's logo and favicon, its version in the footer and the
// pocketone.dev docs link, on every app the set runs (the pocketone binary, an instance on a multisite host, the
// PocketHost mothership). It uses the dashboard's own extension seam, so core is untouched.
func bindBranding(app core.App) {
	ui := brandingUI(pocker.Version())
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			e.UIExtensions = append(e.UIExtensions, ui)
			return e.Next()
		},
	})
}

// brandingUI is the dashboard extension for PocketOne version. The version is written into main.js, which reaches the
// browser inside /_/extensions.js; UI Cache (a standard member) sends that file with no-cache, so an upgrade shows on
// the next page load.
func brandingUI(version string) core.UIExtension {
	logo, err := fs.ReadFile(brandingFiles, "branding/logo.svg")
	if err != nil {
		panic(err) // embedded: missing only if the files were renamed
	}
	tmpl, err := fs.ReadFile(brandingFiles, "branding/main.js")
	if err != nil {
		panic(err)
	}
	if version != "" && version[0] >= '0' && version[0] <= '9' {
		version = "v" + version
	}
	v, _ := json.Marshal(version)
	mainJS := strings.Replace(string(tmpl), `"__POCKETONE_VERSION__"`, string(v), 1)
	now := time.Now()
	return core.UIExtension{Name: brandingName, FS: fstest.MapFS{
		"logo.svg": {Data: logo, ModTime: now},
		"main.js":  {Data: []byte(mainJS), ModTime: now},
	}}
}
