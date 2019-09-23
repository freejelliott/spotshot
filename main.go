package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/http"
	"os"

	mp "monthlyplaylist/pkg/monthlyplaylist"

	"github.com/boj/redistore"
	"github.com/go-redis/redis"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/zmb3/spotify"
)

// Config contains app config details.
type Config struct {
	Spotify struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RedirectURI  string `json:"redirect_uri"`
	}
	App struct {
		Port                             int
		SessionEncryptionKeyFilename     string `json:"session_encryption_key_filename"`
		SessionAuthenticationKeyFilename string `json:"session_authentication_key_filename"`
	}
	Redis struct {
		Addr string
	}
}

var (
	// Version is the current version.
	Version = "no version provided"
	// BuildTime is the RFC-3339 time the current version was built.
	BuildTime = "no build time provided"
)

func main() {
	cfgFilepath := flag.String("c", "cfg/config.json", "path to configuration file")
	versionFlag := flag.Bool("v", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(Version, BuildTime)
		os.Exit(0)
	}

	logger := logrus.New()
	logger.Out = os.Stdout

	// Parse config file.
	var cfg Config
	cfgFile, err := os.Open(*cfgFilepath)
	if err != nil {
		logger.Errorf("err opening cfg file: %s", err)
		os.Exit(1)
	}
	decoder := json.NewDecoder(cfgFile)
	err = decoder.Decode(&cfg)
	if err != nil {
		logger.Errorf("err decoding JSON in config: %s", err)
		os.Exit(1)
	}

	mp.RegisterGobEncodings()

	// Setup Spotify authenticator.
	spotAuth := spotify.NewAuthenticator(cfg.Spotify.RedirectURI,
		spotify.ScopeUserTopRead,
		spotify.ScopePlaylistModifyPrivate,
		spotify.ScopePlaylistModifyPublic)
	spotAuth.SetAuthInfo(cfg.Spotify.ClientID, cfg.Spotify.ClientSecret)

	// Setup session store.
	authKey, err := ioutil.ReadFile(cfg.App.SessionAuthenticationKeyFilename)
	if err != nil {
		logger.Errorf("err reading session authentication key: %s", err)
		os.Exit(1)
	}
	encKey, err := ioutil.ReadFile(cfg.App.SessionEncryptionKeyFilename)
	if err != nil {
		logger.Errorf("err reading session encryption key: %s", err)
		os.Exit(1)
	}
	store, err := redistore.NewRediStore(10, "tcp", cfg.Redis.Addr, "", authKey, encKey)
	if err != nil {
		logger.Errorf("couldn't setup redis session store: %s", err)
		os.Exit(1)
	}
	defer store.Close()

	// Setup Redis client.
	redisClient := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.Addr,
	})
	err = redisClient.Ping().Err()
	if err != nil {
		logger.Errorf("error connecting to redis: %s", err)
		os.Exit(1)
	}

	go mp.PlaylistCreator(context.Background(), redisClient, logger, spotAuth)

	homeTmpl, err := template.ParseFiles("index.html.tmpl")
	if err != nil {
		logger.Errorf("error reading home template: %s", err)
		os.Exit(1)
	}

	// Setup server and specify its endpoints.
	s := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.App.Port),
	}

	r := mux.NewRouter()
	r.Path("/").Methods("GET").Handler(&mp.Endpoint{
		HandlerFunc: mp.Home(homeTmpl, store),
		Logger:      logger})
	r.Path("/login").Methods("POST").Handler(&mp.Endpoint{
		HandlerFunc: mp.SpotifyLogin(spotAuth, store),
		Logger:      logger})
	r.Path("/logout").Methods("POST").Handler(&mp.Endpoint{
		HandlerFunc: mp.Logout(store, logger),
		Logger:      logger})
	r.Path("/callback").Methods("GET").Handler(&mp.Endpoint{
		HandlerFunc: mp.Callback(spotAuth, store, redisClient),
		Logger:      logger})
	r.Path("/subscribe").Methods("POST").Handler(&mp.Endpoint{
		HandlerFunc: mp.Subscribe(store, logger, redisClient),
		Logger:      logger})
	r.Path("/unsubscribe").Methods("POST").Handler(&mp.Endpoint{
		HandlerFunc: mp.Unsubscribe(store, logger, redisClient),
		Logger:      logger})
	s.Handler = r

	logger.Infof("Server running on port %d", cfg.App.Port)
	err = s.ListenAndServe() // Should never go past this.
	if err != http.ErrServerClosed {
		logger.Errorf("server unexpectedly closed: %s", err)
		os.Exit(1)
	}
}
