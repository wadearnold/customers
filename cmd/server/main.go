// Copyright 2019 The Moov Authors
// Use of this source code is governed by an Apache License
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/moov-io/base/admin"
	moovhttp "github.com/moov-io/base/http"
	"github.com/moov-io/base/http/bind"
	"github.com/moov-io/customers"

	"github.com/go-kit/kit/log"
	"github.com/gorilla/mux"
	"github.com/mattn/go-sqlite3"
	"gocloud.dev/blob/fileblob"
)

var (
	httpAddr  = flag.String("http.addr", bind.HTTP("customers"), "HTTP listen address")
	adminAddr = flag.String("admin.addr", bind.Admin("customers"), "Admin HTTP listen address")

	flagLogFormat = flag.String("log.format", "", "Format for log lines (Options: json, plain")
)

func main() {
	flag.Parse()

	var logger log.Logger
	if strings.ToLower(*flagLogFormat) == "json" {
		logger = log.NewJSONLogger(os.Stderr)
	} else {
		logger = log.NewLogfmtLogger(os.Stderr)
	}
	logger = log.With(logger, "ts", log.DefaultTimestampUTC)
	logger = log.With(logger, "caller", log.DefaultCaller)

	logger.Log("startup", fmt.Sprintf("Starting moov-io/customers server version %s", customers.Version))

	// Channel for errors
	errs := make(chan error)

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		errs <- fmt.Errorf("%s", <-c)
	}()

	// Setup SQLite database
	if sqliteVersion, _, _ := sqlite3.Version(); sqliteVersion != "" {
		logger.Log("main", fmt.Sprintf("sqlite version %s", sqliteVersion))
	}
	db, err := createSqliteConnection(logger, getSqlitePath())
	if err != nil {
		logger.Log("main", err)
		os.Exit(1)
	}
	if err := migrate(logger, db); err != nil {
		logger.Log("main", err)
		os.Exit(1)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Log("main", err)
		}
	}()

	customerRepo := &sqliteCustomerRepository{db}
	defer customerRepo.close()
	documentRepo := &sqliteDocumentRepository{db}
	defer documentRepo.close()

	// Create our fileblob.URLSignerHMAC
	bucketName, cloudProvider := os.Getenv("BUCKET_NAME"), strings.ToLower(os.Getenv("CLOUD_PROVIDER"))
	var signer *fileblob.URLSignerHMAC
	if cloudProvider == "file" || cloudProvider == "" {
		if bucketName == "" {
			bucketName = "./storage"
			cloudProvider = "file"
		}

		baseURL, secret := os.Getenv("FILEBLOB_BASE_URL"), os.Getenv("FILEBLOB_HMAC_SECRET")
		if baseURL == "" {
			baseURL = fmt.Sprintf("http://localhost%s/files", bind.HTTP("customers"))
		}
		if secret == "" {
			secret = "secret"
			logger.Log("main", "WARNING!!!! USING INSECURE DEFAULT FILE STORAGE, set FILEBLOB_HMAC_SECRET for ANY production usage")
		}
		s, err := fileblobSigner(baseURL, secret)
		if err != nil {
			panic(fmt.Sprintf("fileBucket: %v", err))
		}
		signer = s
	}

	// Setup business HTTP routes
	router := mux.NewRouter()
	moovhttp.AddCORSHandler(router)
	addPingRoute(router)
	addCustomerRoutes(logger, router, customerRepo)
	addDocumentRoutes(logger, router, documentRepo, getBucket(bucketName, cloudProvider, signer))

	// Optionally serve /files/ as our fileblob routes
	// Note: FILEBLOB_BASE_URL needs to match something that's routed to /files/...
	if cloudProvider == "file" {
		addFileblobRoutes(logger, router, signer, getBucket(bucketName, cloudProvider, signer))
	}

	// Start business HTTP server
	readTimeout, _ := time.ParseDuration("30s")
	writTimeout, _ := time.ParseDuration("30s")
	idleTimeout, _ := time.ParseDuration("60s")

	serve := &http.Server{
		Addr:    *httpAddr,
		Handler: router,
		TLSConfig: &tls.Config{
			InsecureSkipVerify:       false,
			PreferServerCipherSuites: true,
			MinVersion:               tls.VersionTLS12,
		},
		ReadTimeout:  readTimeout,
		WriteTimeout: writTimeout,
		IdleTimeout:  idleTimeout,
	}
	shutdownServer := func() {
		if err := serve.Shutdown(context.TODO()); err != nil {
			logger.Log("shutdown", err)
		}
	}

	// Start Admin server (with Prometheus metrics)
	adminServer := admin.NewServer(*adminAddr)
	go func() {
		logger.Log("admin", fmt.Sprintf("listening on %s", adminServer.BindAddr()))
		if err := adminServer.Listen(); err != nil {
			err = fmt.Errorf("problem starting admin http: %v", err)
			logger.Log("admin", err)
			errs <- err
		}
	}()
	defer adminServer.Shutdown()

	// Register our admin routes
	addApprovalRoutes(logger, adminServer, customerRepo)

	// Start business logic HTTP server
	go func() {
		logger.Log("transport", "HTTP", "addr", *httpAddr)
		errs <- serve.ListenAndServe()
		// TODO(adam): support TLS
		// func (srv *Server) ListenAndServeTLS(certFile, keyFile string) error
	}()

	// Block/Wait for an error
	if err := <-errs; err != nil {
		shutdownServer()
		logger.Log("exit", err)
	}
}

func addPingRoute(r *mux.Router) {
	r.Methods("GET").Path("/ping").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		moovhttp.SetAccessControlAllowHeaders(w, r.Header.Get("Origin"))
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("PONG"))
	})
}