package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	config, err := LoadConfig()
	if err != nil {
		return err
	}
	service, err := newDashboardService(config)
	if err != nil {
		return err
	}
	defer service.Close()
	gin.SetMode(gin.ReleaseMode)
	router := newRouter(config, service)
	server := &http.Server{
		Addr: net.JoinHostPort(config.ListenHost, strconv.Itoa(config.Port)), Handler: router,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second,
	}
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopped)
	errorsFromServer := make(chan error, 1)
	schedulerCtx, stopScheduler := context.WithCancel(context.Background())
	var schedulers sync.WaitGroup
	defer func() {
		stopScheduler()
		schedulers.Wait()
	}()
	schedulers.Add(4)
	go func() {
		defer schedulers.Done()
		service.runAutoGroupScheduler(schedulerCtx, router)
	}()
	go func() {
		defer schedulers.Done()
		service.runRiskScheduler(schedulerCtx)
	}()
	go func() {
		defer schedulers.Done()
		service.runNetworkScheduler(schedulerCtx)
	}()
	go func() {
		defer schedulers.Done()
		service.runIPMaintenanceScheduler(schedulerCtx)
	}()
	go func() { errorsFromServer <- server.ListenAndServe() }()
	log.Printf("site-admin listening on %s (mode=%s)", server.Addr, config.Mode)
	select {
	case err := <-errorsFromServer:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server failed: %w", err)
		}
		return nil
	case <-stopped:
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	}
}
