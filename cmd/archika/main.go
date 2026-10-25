package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"archika/internal/app"
)

func main() {
	if e := run(); e != nil {
		slog.Error("ARChika stopped", "error", e)
		os.Exit(1)
	}
}
func setupLogger() error {
	level := slog.LevelInfo
	switch os.Getenv("ARCHIKA_LOG_LEVEL") {
	case "", "info":
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return errors.New("invalid ARCHIKA_LOG_LEVEL: expected debug, info, warn or error")
	}
	options := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch os.Getenv("ARCHIKA_LOG_FORMAT") {
	case "", "text":
		handler = slog.NewTextHandler(os.Stderr, options)
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, options)
	default:
		return errors.New("invalid ARCHIKA_LOG_FORMAT: expected text or json")
	}
	slog.SetDefault(slog.New(handler))
	return nil
}
func run() error {
	if e := setupLogger(); e != nil {
		return e
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "decoder":
			return app.DecoderMain()
		case "image":
			return app.ImageMain()
		case "plugin-exec":
			if len(os.Args) != 3 {
				return errors.New("plugin executable required")
			}
			return app.PluginExecMain(os.Args[2])
		case "keygen":
			b := make([]byte, 32)
			if _, e := rand.Read(b); e != nil {
				return e
			}
			fmt.Println(base64.StdEncoding.EncodeToString(b))
			return nil
		}
	}
	command := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("archika "+command, flag.ContinueOnError)
	configPath := flags.String("config", os.Getenv("ARCHIKA_CONFIG"), "JSON configuration path")
	name := flags.String("name", "admin", "Initial administrator account name")
	if e := flags.Parse(args); e != nil {
		return e
	}
	c, e := app.LoadConfig(*configPath)
	if e != nil {
		return e
	}
	if command == "admin" {
		password, e := io.ReadAll(io.LimitReader(os.Stdin, 1026))
		if e != nil {
			return e
		}
		lock, e := app.LockDirectory(c.DataDir)
		if e != nil {
			return e
		}
		defer lock.Close()
		db, e := app.OpenDB(c)
		if e != nil {
			return e
		}
		defer db.Close()
		if e = app.BootstrapAdmin(db, *name, strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"), c.Password); e != nil {
			return e
		}
		fmt.Println("Administrator created; no library scan was started.")
		return nil
	}
	if command != "serve" {
		return errors.New("commands: serve, admin (password on stdin), keygen")
	}
	a, e := app.New(c)
	if e != nil {
		return e
	}
	defer func() {
		if e := a.Close(); e != nil {
			slog.Error("close failed", "error", e)
		} else {
			slog.Info("ARChika stopped")
		}
	}()
	if e = app.EnsureInitialAdmin(a.DB, c.Password, slog.Default()); e != nil {
		return e
	}
	listener, e := net.Listen("tcp", c.Listen)
	if e != nil {
		return e
	}
	server := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	a.Start()
	slog.Info("ARChika ready", "listen", listener.Addr().String(), "startup_scan", false)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	select {
	case e := <-stopped:
		if e != http.ErrServerClosed {
			return e
		}
	case signal := <-signals:
		slog.Info("ARChika stopping", "signal", signal.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	}
	return nil
}
