package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/TrueCloudLab/frostfs-node/cmd/frostfs-node/config"
	"github.com/TrueCloudLab/frostfs-node/misc"
	"github.com/TrueCloudLab/frostfs-node/pkg/services/control"
	"go.uber.org/zap"
)

const (
	// SuccessReturnCode returns when application closed without panic.
	SuccessReturnCode = 0
)

// prints err to standard logger and calls os.Exit(1).
func fatalOnErr(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// prints err with details to standard logger and calls os.Exit(1).
func fatalOnErrDetails(details string, err error) {
	if err != nil {
		log.Fatal(fmt.Errorf("%s: %w", details, err))
	}
}

func main() {
	configFile := flag.String("config", "", "path to config")
	configDir := flag.String("config-dir", "", "path to config directory")
	versionFlag := flag.Bool("version", false, "frostfs node version")
	dryRunFlag := flag.Bool("check", false, "validate configuration and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Print(misc.BuildInfo("FrostFS Storage node"))

		os.Exit(SuccessReturnCode)
	}

	appCfg := config.New(config.Prm{}, config.WithConfigFile(*configFile), config.WithConfigDir(*configDir))

	err := validateConfig(appCfg)
	fatalOnErr(err)

	if *dryRunFlag {
		return
	}

	c := initCfg(appCfg)

	initApp(c)

	c.setHealthStatus(control.HealthStatus_STARTING)

	bootUp(c)

	c.setHealthStatus(control.HealthStatus_READY)

	wait(c)
}

func initAndLog(c *cfg, name string, initializer func(*cfg)) {
	c.log.Info(fmt.Sprintf("initializing %s service...", name))
	initializer(c)
	c.log.Info(fmt.Sprintf("%s service has been successfully initialized", name))
}

func initApp(c *cfg) {
	c.ctx, c.ctxCancel = context.WithCancel(context.Background())

	c.wg.Add(1)
	go func() {
		c.signalWatcher()
		c.wg.Done()
	}()

	pprof, _ := pprofComponent(c)
	metrics, _ := metricsComponent(c)
	initAndLog(c, pprof.name, pprof.init)
	initAndLog(c, metrics.name, metrics.init)

	initLocalStorage(c)

	initAndLog(c, "storage engine", func(c *cfg) {
		fatalOnErr(c.cfgObject.cfgLocalStorage.localStorage.Open())
		fatalOnErr(c.cfgObject.cfgLocalStorage.localStorage.Init())
	})

	initAndLog(c, "gRPC", initGRPC)
	initAndLog(c, "netmap", initNetmapService)
	initAndLog(c, "accounting", initAccountingService)
	initAndLog(c, "container", initContainerService)
	initAndLog(c, "session", initSessionService)
	initAndLog(c, "reputation", initReputationService)
	initAndLog(c, "notification", initNotifications)
	initAndLog(c, "object", initObjectService)
	initAndLog(c, "tree", initTreeService)
	initAndLog(c, "control", initControlService)

	initAndLog(c, "morph notifications", listenMorphNotifications)
}

func runAndLog(c *cfg, name string, logSuccess bool, starter func(*cfg)) {
	c.log.Info(fmt.Sprintf("starting %s service...", name))
	starter(c)

	if logSuccess {
		c.log.Info(fmt.Sprintf("%s service started successfully", name))
	}
}

func stopAndLog(c *cfg, name string, stopper func() error) {
	c.log.Debug(fmt.Sprintf("shutting down %s service", name))

	err := stopper()
	if err != nil {
		c.log.Debug(fmt.Sprintf("could not shutdown %s server", name),
			zap.String("error", err.Error()),
		)
	}

	c.log.Debug(fmt.Sprintf("%s service has been stopped", name))
}

func bootUp(c *cfg) {
	runAndLog(c, "NATS", true, connectNats)
	runAndLog(c, "gRPC", false, serveGRPC)
	runAndLog(c, "notary", true, makeAndWaitNotaryDeposit)

	bootstrapNode(c)
	startWorkers(c)
}

func wait(c *cfg) {
	c.log.Info("application started",
		zap.String("version", misc.Version))

	<-c.ctx.Done() // graceful shutdown

	c.log.Debug("waiting for all processes to stop")

	c.wg.Wait()
}

func (c *cfg) onShutdown(f func()) {
	c.closers = append(c.closers, closer{"", f})
}
