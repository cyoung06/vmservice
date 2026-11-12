// vmservice is a self-service portal for KubeVirt VMs: members sign in with OpenID Connect and
// create, start, stop and delete their own machines, and open their serial and VNC consoles in the
// browser. Every machine lives in one namespace, labeled with its owner; members never get
// Kubernetes credentials.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/cyoung06/vmservice/internal/api"
	"github.com/cyoung06/vmservice/internal/auth"
	"github.com/cyoung06/vmservice/internal/config"
	"github.com/cyoung06/vmservice/internal/console"
	"github.com/cyoung06/vmservice/internal/machines"
	"github.com/cyoung06/vmservice/internal/web"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	var a auth.Config
	var ui web.Config
	listen := flag.String("listen", ":8080", "listen address (plain HTTP; TLS terminates in front)")
	namespace := flag.String("namespace", "", "the namespace of the machines and the images (required)")
	catalogFile := flag.String("catalog", "", "the catalog file: images, sizes, disk range, limits (required)")
	flag.StringVar(&a.Origin, "origin", "", "the public origin, e.g. https://vm.example.org (required)")
	flag.StringVar(&a.Issuer, "oidc-issuer", "", "the OpenID Connect issuer URL (required)")
	flag.StringVar(&a.ClientID, "oidc-client-id", "", "the OIDC client id (required)")
	clientSecretFile := flag.String("oidc-client-secret-file", "", "file holding the OIDC client secret (required)")
	flag.StringVar(&a.Group, "group", "", "group the ID token must list to sign in (empty: anyone the provider signs in)")
	flag.StringVar(&a.GroupsClaim, "groups-claim", "groups", "the ID token claim with the user's groups")
	flag.StringVar(&a.UsernameClaim, "username-claim", "preferred_username", "the ID token claim with the username")
	flag.StringVar(&a.KeyFile, "session-key-file", "", "file holding the 32-byte session cookie key, raw or base64 (required)")
	flag.DurationVar(&a.Lifetime, "session-lifetime", 7*24*time.Hour, "how long a sign-in lasts; also how long a removed member keeps access")
	maxStreams := flag.Int("max-streams", 8, "open consoles and live views per member, together")
	flag.StringVar(&ui.Title, "title", "VMService", "name shown in the sidebar and the tab title")
	flag.StringVar(&ui.LoginProvider, "login-provider", "", "the sign-in provider's name for the button, e.g. \"Keycloak\" (empty: \"Sign in\")")
	flag.Parse()

	for name, v := range map[string]string{"namespace": *namespace, "catalog": *catalogFile, "origin": a.Origin, "oidc-issuer": a.Issuer,
		"oidc-client-id": a.ClientID, "oidc-client-secret-file": *clientSecretFile, "session-key-file": a.KeyFile} {
		if v == "" {
			log.Error("-" + name + " is required")
			os.Exit(2)
		}
	}
	a.Origin = strings.TrimRight(a.Origin, "/")
	secret, err := os.ReadFile(*clientSecretFile)
	if err != nil {
		log.Error("client secret", "err", err)
		os.Exit(1)
	}
	a.ClientSecret = strings.TrimSpace(string(secret))
	catalog, err := config.Load(*catalogFile)
	if err != nil {
		log.Error("catalog", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	signIn, err := auth.New(ctx, a, log)
	if err != nil {
		log.Error("sign-in", "err", err)
		os.Exit(1)
	}
	kube, err := ctrlconfig.GetConfig()
	if err != nil {
		log.Error("kubernetes config", "err", err)
		os.Exit(1)
	}
	vms, err := machines.New(ctx, kube, *namespace, catalog, log)
	if err != nil {
		log.Error("machines", "err", err)
		os.Exit(1)
	}
	proxy, err := console.New(kube, machines.APIServer(kube))
	if err != nil {
		log.Error("console", "err", err)
		os.Exit(1)
	}
	h, err := web.Handler(web.UI(), ui, a.Origin, signIn.Routes, api.New(signIn, vms, proxy, a.Origin, *maxStreams, log).Routes)
	if err != nil {
		log.Error("ui", "err", err)
		os.Exit(1)
	}
	// The event stream and the consoles are long-lived: each clears these deadlines for itself.
	srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: time.Minute}
	go func() { <-ctx.Done(); srv.Close() }()
	log.Info("vmservice listening", "addr", *listen, "version", version, "namespace", *namespace)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
