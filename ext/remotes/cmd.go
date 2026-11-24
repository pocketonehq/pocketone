package remotes

import (
	"context"
	"fmt"
	"io"
	"os"
	remoteclient "pocketone.dev/ext/contracts/remote/v1/remoteclient"
	"pocketone.dev/ext/extkit"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// BrowserLogin is tried first by `login` on remotes that offer OAuth (tests replace it). It returns (nil, nil) to fall
// back to the token prompt.
var BrowserLogin = remoteclient.OAuthLogin

// Commands returns the client commands: remote, login, logout, link.
func Commands() []*cobra.Command {
	return []*cobra.Command{remoteCommand(), loginCommand(), logoutCommand(), linkCommand()}
}

func cwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}

func remoteCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "remote", Short: "The project's remotes, and the commands that run on them (every contract an app offers)"}

	var makeDefault bool
	add := &cobra.Command{
		Use: "add <name> <url>", Short: "Adds or replaces a remote", Args: cobra.ExactArgs(2), SilenceUsage: true,
		Example: "remote add prod https://myapp.example.com",
		RunE: func(c *cobra.Command, args []string) error {
			p, err := remoteclient.LoadProject(cwd())
			if err != nil {
				return err
			}
			r, err := p.Add(args[0], args[1])
			if err != nil {
				return err
			}
			if makeDefault {
				p.Default = r.Name
			}
			if err := p.Save(); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Added %s → %s. Next: %s login %s\n", r.Name, r.URL, extkit.Command, r.Name)
			return nil
		},
	}
	add.Flags().BoolVar(&makeDefault, "default", false, "make it the default remote")

	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "Lists remotes and whether you're logged in", SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			p, err := remoteclient.LoadProject(cwd())
			if err != nil {
				return err
			}
			creds, err := remoteclient.LoadCredentials()
			if err != nil {
				return err
			}
			if len(p.Remotes) == 0 {
				fmt.Fprintln(c.OutOrStdout(), "No remotes. Add one: "+extkit.Command+" remote add prod https://myapp.example.com")
				return nil
			}
			w := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "\tNAME\tURL\tLOGIN")
			for _, n := range p.Names() {
				r := p.Remotes[n]
				mark := ""
				if n == p.Default {
					mark = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", mark, n, r.URL, describeLogin(n, creds.ByURL[r.URL]))
			}
			return w.Flush()
		},
	}

	rm := &cobra.Command{
		Use: "rm <name>", Aliases: []string{"remove"}, Short: "Removes a remote (its saved login stays; see logout)",
		Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			p, err := remoteclient.LoadProject(cwd())
			if err != nil {
				return err
			}
			if err := p.Remove(args[0]); err != nil {
				return err
			}
			return p.Save()
		},
	}

	def := &cobra.Command{
		Use: "default <name>", Short: "Sets the default remote", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			p, err := remoteclient.LoadProject(cwd())
			if err != nil {
				return err
			}
			if _, ok := p.Remotes[args[0]]; !ok {
				return fmt.Errorf("no remote %q", args[0])
			}
			p.Default = args[0]
			return p.Save()
		},
	}

	whoami := &cobra.Command{
		Use: "whoami [remote]", Short: "Shows who you are on a remote and what it offers", Args: cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			cl, err := remoteclient.Open(cwd(), first(args))
			if err != nil && cl == nil {
				return err
			}
			ctx, cancel := context.WithTimeout(c.Context(), 20*time.Second)
			defer cancel()
			w, werr := cl.Whoami(ctx)
			if werr != nil {
				return werr
			}
			out := c.OutOrStdout()
			fmt.Fprintf(out, "%s (%s)\n", cl.Remote.Name, cl.Remote.URL)
			if w.Auth == nil {
				fmt.Fprintln(out, "  not logged in")
			} else {
				sc := "all"
				if len(w.Scopes) > 0 {
					sc = strings.Join(w.Scopes, ", ")
				}
				fmt.Fprintf(out, "  %s in %s, via %s (%s), scopes: %s\n", w.Auth.Email, w.Auth.Collection, w.Via, cl.TokenSource, sc)
			}
			for _, c := range w.Contracts {
				fmt.Fprintf(out, "  offers %s at /api/_c/%s\n", c, c)
			}
			return nil
		},
	}

	cmd.AddCommand(add, list, rm, def, whoami)
	return cmd
}

func describeLogin(name string, cr *remoteclient.Credential) string {
	if tok, src := remoteclient.EnvToken(); tok != "" {
		return src
	}
	if cr == nil {
		return "-"
	}
	s := cr.Via
	if cr.Email != "" {
		s = cr.Email + " (" + cr.Via + ")"
	}
	if !cr.Expires.IsZero() {
		if time.Now().After(cr.Expires) {
			s += ", expired"
		} else {
			s += ", until " + cr.Expires.Format("2006-01-02")
		}
	}
	return s
}

func first(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

type loginFlags struct {
	token      string
	tokenStdin bool
	device     bool
	noBrowser  bool
}

func (f *loginFlags) bind(c *cobra.Command) {
	c.Flags().StringVar(&f.token, "token", "", "use this API token (pbt_..., a PocketHost token, or a JWT)")
	c.Flags().BoolVar(&f.tokenStdin, "token-stdin", false, "read the token from stdin")
	c.Flags().BoolVar(&f.device, "device", false, "sign in on another device with a code (SSH, headless)")
	c.Flags().BoolVar(&f.noBrowser, "no-browser", false, "print the sign-in URL instead of opening a browser")
}

func loginCommand() *cobra.Command {
	var f loginFlags
	c := &cobra.Command{
		Use:   "login [remote]",
		Short: "Signs in to a remote and saves the login",
		Long: "Signs in to a remote: in the browser when the remote offers OAuth (--device for a code on another device), " +
			"else with a superuser's email and password (or a pasted API token).",
		Args: cobra.MaximumNArgs(1), SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			cl, err := remoteclient.Open(cwd(), first(args))
			if err != nil && cl == nil {
				return err
			}
			return runLogin(c, cl, f)
		},
	}
	f.bind(c)
	return c
}

func linkCommand() *cobra.Command {
	var (
		f    loginFlags
		name string
	)
	c := &cobra.Command{
		Use:     "link <url> [token]",
		Short:   "Adds a remote (default name \"default\") and signs in to it",
		Example: "link https://myapp.example.com\nlink https://myapp.pockethost.io ph_pat_...",
		Args:    cobra.RangeArgs(1, 2), SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			p, err := remoteclient.LoadProject(cwd())
			if err != nil {
				return err
			}
			r, err := p.Add(name, args[0])
			if err != nil {
				return err
			}
			if p.Default == "" || len(p.Remotes) == 1 {
				p.Default = r.Name
			}
			if err := p.Save(); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Linked %s → %s\n", r.Name, r.URL)
			if len(args) == 2 {
				f.token = args[1]
			}
			return runLogin(c, &remoteclient.Client{Remote: r, HTTP: remoteclient.NewHTTPClient()}, f)
		},
	}
	c.Flags().StringVar(&name, "name", remoteclient.DefaultName, "remote name")
	f.bind(c)
	return c
}

func runLogin(c *cobra.Command, cl *remoteclient.Client, f loginFlags) error {
	ctx, cancel := context.WithTimeout(c.Context(), 10*time.Minute)
	defer cancel()
	opts := remoteclient.LoginOptions{Token: f.token, Device: f.device, NoBrowser: f.noBrowser, Out: c.OutOrStdout()}
	if BrowserLogin != nil {
		opts.Browser = BrowserLogin
	}
	if f.tokenStdin {
		opts.TokenFrom = c.InOrStdin()
	}
	if isTerminal(c.InOrStdin()) {
		opts.Prompt = c.InOrStdin()
		fd := int(c.InOrStdin().(*os.File).Fd())
		opts.Password = func(prompt string) (string, error) {
			fmt.Fprint(c.OutOrStdout(), prompt)
			b, err := term.ReadPassword(fd)
			fmt.Fprintln(c.OutOrStdout())
			return string(b), err
		}
	}
	_, err := remoteclient.Login(ctx, cl, opts)
	return err
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func logoutCommand() *cobra.Command {
	return &cobra.Command{
		Use: "logout [remote]", Short: "Forgets the saved login (and revokes it, for a browser login)",
		Args: cobra.MaximumNArgs(1), SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			p, err := remoteclient.LoadProject(cwd())
			if err != nil {
				return err
			}
			r, err := p.Resolve(first(args))
			if err != nil {
				return err
			}
			creds, err := remoteclient.LoadCredentials()
			if err != nil {
				return err
			}
			cr := creds.ByURL[r.URL]
			if cr == nil {
				fmt.Fprintf(c.OutOrStdout(), "Not logged in to %s.\n", r.URL)
				return nil
			}
			if cr.Via == "oauth" {
				ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
				if err := remoteclient.RevokeOAuth(ctx, &remoteclient.Client{Remote: r, HTTP: remoteclient.NewHTTPClient()}, cr); err != nil {
					fmt.Fprintf(c.ErrOrStderr(), "Couldn't revoke the login on %s (%v); revoke it in the dashboard.\n", r.URL, err)
				}
				cancel()
			}
			delete(creds.ByURL, r.URL)
			if err := creds.Save(); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Logged out of %s.\n", r.URL)
			return nil
		},
	}
}
