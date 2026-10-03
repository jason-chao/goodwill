// Command goodwill is a small, self-hosted web analytics server.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // sites have timezones; do not depend on the host's database

	"golang.org/x/term"

	"github.com/jason-chao/goodwill/internal/auth"
	"github.com/jason-chao/goodwill/internal/config"
	"github.com/jason-chao/goodwill/internal/dashboard"
	"github.com/jason-chao/goodwill/internal/geo"
	"github.com/jason-chao/goodwill/internal/identity"
	"github.com/jason-chao/goodwill/internal/ingest"
	"github.com/jason-chao/goodwill/internal/store"
	"github.com/jason-chao/goodwill/tracker"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `goodwill is a small, self-hosted web analytics server.

Usage:
  goodwill serve                      run the server
  goodwill migrate                    create or upgrade the database, then exit
  goodwill site add --name NAME       add a site and print its tracking snippet
  goodwill site list                  list sites
  goodwill site set ID [options]      change a site's settings
  goodwill site key ID                issue a new ingest key for a site
  goodwill user add USERNAME          create a dashboard user or reset its password
  goodwill backup FILE                write a consistent copy of the database
  goodwill version                    print the version

Every command accepts -config FILE (or the GOODWILL_CONFIG environment
variable). Without one, built-in defaults and GOODWILL_* variables are used.
Run a command with -h to see its options.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "serve":
		err = cmdServe(args)
	case "migrate":
		err = cmdMigrate(args)
	case "site":
		err = cmdSite(args)
	case "user":
		err = cmdUser(args)
	case "backup":
		err = cmdBackup(args)
	case "version", "-v", "--version":
		fmt.Println("goodwill", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "goodwill: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "goodwill:", err)
		os.Exit(1)
	}
}

// newFlags returns a flag set with the shared -config option.
func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("goodwill "+name, flag.ContinueOnError)
	path := fs.String("config", os.Getenv("GOODWILL_CONFIG"), "path to the configuration `file`")
	return fs, path
}

func open(configPath string) (config.Config, *store.Store, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return cfg, nil, err
	}
	st, err := store.Open(cfg.Database.Path)
	return cfg, st, err
}

func cmdMigrate(args []string) error {
	fs, path := newFlags("migrate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, st, err := open(*path)
	if err != nil {
		return err
	}
	defer st.Close()
	v, err := st.SchemaVersion()
	if err != nil {
		return err
	}
	fmt.Printf("%s is at schema version %d\n", cfg.Database.Path, v)
	return nil
}

func cmdBackup(args []string) error {
	fs, path := newFlags("backup")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: goodwill backup [-config FILE] DEST")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return flag.ErrHelp
	}
	dest := fs.Arg(0)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists; choose a new file name", dest)
	}
	_, st, err := open(*path)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Backup(context.Background(), dest); err != nil {
		return err
	}
	fmt.Println("backup written to", dest)
	return nil
}

func cmdUser(args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return errors.New("usage: goodwill user add [-config FILE] [-password-stdin] USERNAME")
	}
	fs, path := newFlags("user add")
	fromStdin := fs.Bool("password-stdin", false, "read the password from standard input instead of prompting")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: goodwill user add [-config FILE] [-password-stdin] USERNAME")
	}
	username := strings.TrimSpace(fs.Arg(0))
	if username == "" {
		return errors.New("username must not be empty")
	}
	password, err := readPassword(*fromStdin)
	if err != nil {
		return err
	}
	if len(password) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, st, err := open(*path)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.UpsertUser(username, hash); err != nil {
		return err
	}
	fmt.Printf("user %q saved\n", username)
	return nil
}

func readPassword(fromStdin bool) (string, error) {
	fd := int(os.Stdin.Fd())
	if fromStdin || !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Again: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}

func cmdServe(args []string) error {
	fs, path := newFlags("serve")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, st, err := open(*path)
	if err != nil {
		return err
	}
	st.StartWriter()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	gdb, err := geo.Open(cfg.Geo.Path)
	if err != nil {
		st.Close()
		return err
	}
	defer gdb.Close()
	geoNote := cfg.Geo.Attribution
	switch {
	case gdb == nil:
		slog.Warn("no location database: countries will not be recorded; set geo.path to an .mmdb file")
	case cfg.Geo.Path == "":
		geoNote = "IP geolocation by DB-IP (https://db-ip.com)"
		slog.Info("location lookups enabled", "source", gdb.Source)
	default:
		slog.Info("location lookups enabled", "source", gdb.Source)
	}

	ident := identity.NewManager(st)
	const visitsKey = "open_visits"
	if saved, err := st.GetKV(visitsKey); err == nil && saved != nil {
		if err := ident.Restore(saved, time.Now().Unix()); err != nil {
			slog.Warn("could not restore open visits", "err", err)
		}
		st.DeleteKV(visitsKey)
	}

	in, err := ingest.New(cfg, st, ident, gdb, tracker.Script)
	if err != nil {
		st.Close()
		return err
	}
	dash, err := dashboard.New(st, &auth.Sessions{Store: st, Secure: cfg.Server.SecureCookies}, version, geoNote, cfg.Server.PublicURL)
	if err != nil {
		st.Close()
		return err
	}

	// The standard server logs client addresses when a connection fails. Those
	// lines are dropped; panics are logged by recovered() without an address.
	quiet := log.New(io.Discard, "", 0)
	public := &http.Server{
		Addr: cfg.Server.PublicListen, Handler: recovered(in.Routes()), ErrorLog: quiet,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	admin := &http.Server{
		Addr: cfg.Server.AdminListen, Handler: recovered(dash.Routes()), ErrorLog: quiet,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10,
	}

	publicLn, err := net.Listen("tcp", cfg.Server.PublicListen)
	if err != nil {
		st.Close()
		return fmt.Errorf("public listener: %w", err)
	}
	adminLn, err := net.Listen("tcp", cfg.Server.AdminListen)
	if err != nil {
		publicLn.Close()
		st.Close()
		return fmt.Errorf("admin listener: %w", err)
	}

	if isLoopback(cfg.Server.PublicListen) && len(cfg.Server.TrustedProxies) == 0 {
		slog.Warn("the public listener is on loopback with no trusted proxies: if a reverse proxy is in front, " +
			"every visitor will look like the proxy; add its address to server.trusted_proxies")
	}
	if !isLoopback(cfg.Server.AdminListen) {
		slog.Warn("the dashboard is not on loopback: make sure this address is reachable only from a private network",
			"admin_listen", cfg.Server.AdminListen)
	}
	slog.Info("goodwill started", "version", version, "public", cfg.Server.PublicListen,
		"admin", cfg.Server.AdminListen, "database", cfg.Database.Path)

	errc := make(chan error, 2)
	go func() { errc <- public.Serve(publicLn) }()
	go func() { errc <- admin.Serve(adminLn) }()

	stopTick := make(chan struct{})
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for n := 0; ; n++ {
			select {
			case <-tick.C:
				ident.Evict(time.Now().Unix())
				if n%60 == 0 {
					st.PruneAuthSessions()
				}
			case <-stopTick:
				return
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	var serveErr error
	select {
	case <-sig:
		slog.Info("shutting down")
	case serveErr = <-errc:
	}
	close(stopTick)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	public.Shutdown(ctx)
	admin.Shutdown(ctx)

	// Carry open visits across the restart. The snapshot holds visitor hashes
	// and visit IDs only.
	if snap, err := ident.Snapshot(); err == nil {
		st.Flush()
		if err := st.SetKV(visitsKey, snap); err != nil {
			slog.Warn("could not save open visits", "err", err)
		}
	}
	if err := st.Close(); err != nil {
		return err
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// recovered turns a panic in a handler into a 500 and a log line that names
// the route but not the client.
func recovered(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil && v != http.ErrAbortHandler {
				slog.Error("handler panicked", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(v))
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func cmdSite(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: goodwill site add|list|set|key [options]")
	}
	switch args[0] {
	case "add":
		return siteAdd(args[1:])
	case "list":
		return siteList(args[1:])
	case "set":
		return siteSet(args[1:])
	case "key":
		return siteKey(args[1:])
	}
	return fmt.Errorf("unknown site command %q: want add, list, set or key", args[0])
}

func splitList(v string) []string {
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func siteAdd(args []string) error {
	fs, path := newFlags("site add")
	name := fs.String("name", "", "display name (required)")
	domains := fs.String("domains", "", "comma-separated hostnames allowed to send events; empty allows any")
	tz := fs.String("timezone", "UTC", "IANA timezone used for days and hours, e.g. Europe/London")
	interval := fs.String("salt-interval", "day", "how long a visitor can be recognised: day, week, month or <n>d")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, st, err := open(*path)
	if err != nil {
		return err
	}
	defer st.Close()
	site := &store.Site{
		Name: strings.TrimSpace(*name), Domains: splitList(*domains), Timezone: *tz,
		SaltInterval: *interval, Settings: store.DefaultSettings(),
	}
	if err := st.CreateSite(site); err != nil {
		return err
	}
	fmt.Printf("site %d added: %s\n\nWebsite ID: %s\n\nAdd this to your pages, replacing the address with your server's:\n\n"+
		"  <script defer src=\"https://stats.example.com/script.js\" data-website-id=\"%s\"></script>\n",
		site.ID, site.Name, site.PublicID, site.PublicID)
	return nil
}

func siteList(args []string) error {
	fs, path := newFlags("site list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, st, err := open(*path)
	if err != nil {
		return err
	}
	defer st.Close()
	sites, err := st.Sites()
	if err != nil {
		return err
	}
	if len(sites) == 0 {
		fmt.Println("no sites yet; add one with: goodwill site add --name NAME")
		return nil
	}
	for _, s := range sites {
		domains := "any"
		if len(s.Domains) > 0 {
			domains = strings.Join(s.Domains, ",")
		}
		fmt.Printf("%d\t%s\t%s\tdomains=%s\ttimezone=%s\tsalt=%s\tlocation=%s\n",
			s.ID, s.Name, s.PublicID, domains, s.Timezone, s.SaltInterval, s.Settings.Collect.Location)
	}
	return nil
}

func onOff(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("%q: want on or off", v)
}

func siteSet(args []string) error {
	fs, path := newFlags("site set")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: goodwill site set ID [options]\n\nOnly the options you give are changed.")
		fs.PrintDefaults()
	}
	name := fs.String("name", "", "display name")
	domains := fs.String("domains", "", "comma-separated hostnames allowed to send events; \"any\" allows all")
	tz := fs.String("timezone", "", "IANA timezone; applies to events recorded from now on")
	interval := fs.String("salt-interval", "", "day, week, month or <n>d; takes effect when the current interval ends")
	location := fs.String("location", "", "location detail to keep: none, country, region or city")
	ipMode := fs.String("ip-mode", "", "full (hash address and browser) or masked (hash a shortened address only)")
	ignore := fs.String("ignore-ips", "", "comma-separated addresses or CIDR ranges to discard; \"none\" clears")
	trusted := fs.String("trusted-sources", "", "comma-separated addresses or CIDR ranges of your own servers; \"none\" clears")
	keep := fs.String("keep-query", "", "comma-separated query-string keys kept in page paths; \"none\" clears")
	collect := fs.String("collect", "", "switches, e.g. identify=on,referrer=off. Names: referrer, user_agent, "+
		"screen, language, utm, props, identify, performance")

	// Accept the ID before or after the options.
	var id string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if id == "" && fs.NArg() == 1 {
		id = fs.Arg(0)
	}
	siteID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		fs.Usage()
		return flag.ErrHelp
	}
	_, st, err := open(*path)
	if err != nil {
		return err
	}
	defer st.Close()
	site, err := st.Site(siteID)
	if err != nil {
		return err
	}
	if site == nil {
		return fmt.Errorf("no site with ID %d", siteID)
	}

	list := func(v string, dst *[]string) {
		switch v {
		case "":
		case "none", "any":
			*dst = []string{}
		default:
			*dst = splitList(v)
		}
	}
	if *name != "" {
		site.Name = *name
	}
	if *tz != "" {
		site.Timezone = *tz
	}
	if *interval != "" {
		site.SaltInterval = *interval
	}
	if *location != "" {
		site.Settings.Collect.Location = *location
	}
	if *ipMode != "" {
		site.Settings.IPMode = *ipMode
	}
	list(*domains, &site.Domains)
	list(*ignore, &site.Settings.IgnoreIPs)
	list(*trusted, &site.Settings.TrustedSources)
	list(*keep, &site.Settings.KeepQuery)
	for _, lst := range [][]string{site.Settings.IgnoreIPs, site.Settings.TrustedSources} {
		if _, err := config.ParsePrefixes(lst); err != nil {
			return err
		}
	}
	for _, pair := range splitList(*collect) {
		key, value, _ := strings.Cut(pair, "=")
		on, err := onOff(value)
		if err != nil {
			return fmt.Errorf("-collect %s: %w", pair, err)
		}
		c := &site.Settings.Collect
		switch key {
		case "referrer":
			c.Referrer = on
		case "user_agent":
			c.UserAgent = on
		case "screen":
			c.Screen = on
		case "language":
			c.Language = on
		case "utm":
			c.UTM = on
		case "props":
			c.Props = on
		case "identify":
			c.Identify = on
		case "performance":
			c.Performance = on
		default:
			return fmt.Errorf("-collect: unknown switch %q", key)
		}
	}
	if err := st.UpdateSite(site); err != nil {
		return err
	}
	fmt.Printf("site %d updated; a running server picks the change up within 15 seconds\n", site.ID)
	return nil
}

func siteKey(args []string) error {
	fs, path := newFlags("site key")
	var id string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if id == "" && fs.NArg() == 1 {
		id = fs.Arg(0)
	}
	siteID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return errors.New("usage: goodwill site key ID")
	}
	_, st, err := open(*path)
	if err != nil {
		return err
	}
	defer st.Close()
	site, err := st.Site(siteID)
	if err != nil {
		return err
	}
	if site == nil {
		return fmt.Errorf("no site with ID %d", siteID)
	}
	key, err := st.NewIngestKey(siteID)
	if err != nil {
		return err
	}
	fmt.Printf("New ingest key for site %d (%s). Any earlier key no longer works.\nIt is shown once:\n\n  %s\n\n"+
		"Send it as \"Authorization: Bearer <key>\" from your own servers.\n", site.ID, site.Name, key)
	return nil
}
