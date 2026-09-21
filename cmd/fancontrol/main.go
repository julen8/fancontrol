package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/julen8/fancontrol/internal/app"
	"github.com/julen8/fancontrol/internal/config"
	"github.com/julen8/fancontrol/internal/hardware"
	webui "github.com/julen8/fancontrol/internal/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fancontrol:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "./fancontrol.toml", "path to the TOML configuration file")
	checkConfig := flag.Bool("check-config", false, "validate the configuration and exit")
	resetPassword := flag.Bool("reset-password", false, "generate a new administrator password and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	configuration, credentials, err := config.LoadOrCreate(*configPath)
	if err != nil {
		return err
	}
	if *resetPassword {
		credentials, err = config.ResetPassword(*configPath, configuration)
		if err != nil {
			return err
		}
		printCredentials(*configPath, credentials)
		return nil
	}
	if credentials != nil {
		printCredentials(*configPath, credentials)
	}
	if *checkConfig {
		fmt.Printf("configuration is valid: %s\n", *configPath)
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	manager := app.New(
		configuration,
		*configPath,
		hardware.NewCPUReader(),
		hardware.NewDiskService(),
		hardware.NewIPMI(configuration.IPMI.Command, configuration.IPMI.Timeout.Duration),
		logger,
	)

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalContext)
	defer cancel()

	managerDone := make(chan struct{})
	go func() {
		defer close(managerDone)
		manager.Run(ctx)
	}()
	server := &http.Server{
		Addr:              configuration.Server.Listen,
		Handler:           webui.New(manager, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("web server started", "address", "http://"+configuration.Server.Listen, "config", *configPath)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-signalContext.Done():
		logger.Info("shutdown requested")
	case serverErr := <-serverErrors:
		if !errors.Is(serverErr, http.ErrServerClosed) {
			err = serverErr
		}
	}
	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	shutdownErr := server.Shutdown(shutdownContext)
	select {
	case <-managerDone:
	case <-shutdownContext.Done():
		return fmt.Errorf("fan shutdown timed out: %w", shutdownContext.Err())
	}
	if err != nil {
		return err
	}
	return shutdownErr
}

func printCredentials(path string, credentials *config.InitialCredentials) {
	fmt.Printf("Created initial web account in %s\nUsername: %s\nPassword: %s\nChange this password after signing in.\n", path, credentials.Username, credentials.Password)
}
