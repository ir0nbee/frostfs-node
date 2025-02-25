package main

import (
	"context"
	"fmt"

	v2reputation "github.com/TrueCloudLab/frostfs-api-go/v2/reputation"
	v2reputationgrpc "github.com/TrueCloudLab/frostfs-api-go/v2/reputation/grpc"
	"github.com/TrueCloudLab/frostfs-api-go/v2/session"
	"github.com/TrueCloudLab/frostfs-node/cmd/frostfs-node/reputation/common"
	intermediatereputation "github.com/TrueCloudLab/frostfs-node/cmd/frostfs-node/reputation/intermediate"
	localreputation "github.com/TrueCloudLab/frostfs-node/cmd/frostfs-node/reputation/local"
	"github.com/TrueCloudLab/frostfs-node/cmd/frostfs-node/reputation/ticker"
	repClient "github.com/TrueCloudLab/frostfs-node/pkg/morph/client/reputation"
	"github.com/TrueCloudLab/frostfs-node/pkg/morph/event"
	"github.com/TrueCloudLab/frostfs-node/pkg/morph/event/netmap"
	grpcreputation "github.com/TrueCloudLab/frostfs-node/pkg/network/transport/reputation/grpc"
	"github.com/TrueCloudLab/frostfs-node/pkg/services/reputation"
	reputationcommon "github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/common"
	reputationrouter "github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/common/router"
	"github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/eigentrust"
	eigentrustcalc "github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/eigentrust/calculator"
	eigentrustctrl "github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/eigentrust/controller"
	intermediateroutes "github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/eigentrust/routes"
	consumerstorage "github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/eigentrust/storage/consumers"
	"github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/eigentrust/storage/daughters"
	localtrustcontroller "github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/local/controller"
	localroutes "github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/local/routes"
	truststorage "github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/local/storage"
	reputationrpc "github.com/TrueCloudLab/frostfs-node/pkg/services/reputation/rpc"
	"github.com/TrueCloudLab/frostfs-node/pkg/util/logger"
	apireputation "github.com/TrueCloudLab/frostfs-sdk-go/reputation"
	"go.uber.org/zap"
)

func initReputationService(c *cfg) {
	wrap, err := repClient.NewFromMorph(c.cfgMorph.client, c.cfgReputation.scriptHash, 0, repClient.TryNotary())
	fatalOnErr(err)

	localKey := c.key.PublicKey().Bytes()

	nmSrc := c.netMapSource

	// storing calculated trusts as a daughter
	c.cfgReputation.localTrustStorage = truststorage.New(
		truststorage.Prm{},
	)

	daughterStorage := daughters.New(daughters.Prm{})
	consumerStorage := consumerstorage.New(consumerstorage.Prm{})

	// storing received daughter(of current node) trusts as a manager
	daughterStorageWriterProvider := &intermediatereputation.DaughterStorageWriterProvider{
		Log:     c.log,
		Storage: daughterStorage,
	}

	consumerStorageWriterProvider := &intermediatereputation.ConsumerStorageWriterProvider{
		Log:     c.log,
		Storage: consumerStorage,
	}

	localTrustLogger := &logger.Logger{Logger: c.log.With(zap.String("trust_type", "local"))}
	intermediateTrustLogger := &logger.Logger{Logger: c.log.With(zap.String("trust_type", "intermediate"))}

	localTrustStorage := &localreputation.TrustStorage{
		Log:      localTrustLogger,
		Storage:  c.cfgReputation.localTrustStorage,
		NmSrc:    nmSrc,
		LocalKey: localKey,
	}

	managerBuilder := reputationcommon.NewManagerBuilder(
		reputationcommon.ManagersPrm{
			NetMapSource: nmSrc,
		},
		reputationcommon.WithLogger(c.log),
	)

	localRouteBuilder := localroutes.New(
		localroutes.Prm{
			ManagerBuilder: managerBuilder,
			Log:            localTrustLogger,
		},
	)

	intermediateRouteBuilder := intermediateroutes.New(
		intermediateroutes.Prm{
			ManagerBuilder: managerBuilder,
			Log:            intermediateTrustLogger,
		},
	)

	remoteLocalTrustProvider := common.NewRemoteTrustProvider(
		common.RemoteProviderPrm{
			NetmapKeys:      c,
			DeadEndProvider: daughterStorageWriterProvider,
			ClientCache:     c.bgClientCache,
			WriterProvider: localreputation.NewRemoteProvider(
				localreputation.RemoteProviderPrm{
					Key: &c.key.PrivateKey,
					Log: localTrustLogger,
				},
			),
			Log: localTrustLogger,
		},
	)

	remoteIntermediateTrustProvider := common.NewRemoteTrustProvider(
		common.RemoteProviderPrm{
			NetmapKeys:      c,
			DeadEndProvider: consumerStorageWriterProvider,
			ClientCache:     c.bgClientCache,
			WriterProvider: intermediatereputation.NewRemoteProvider(
				intermediatereputation.RemoteProviderPrm{
					Key: &c.key.PrivateKey,
					Log: intermediateTrustLogger,
				},
			),
			Log: intermediateTrustLogger,
		},
	)

	localTrustRouter := reputationrouter.New(
		reputationrouter.Prm{
			LocalServerInfo:      c,
			RemoteWriterProvider: remoteLocalTrustProvider,
			Builder:              localRouteBuilder,
		},
		reputationrouter.WithLogger(localTrustLogger))

	intermediateTrustRouter := reputationrouter.New(
		reputationrouter.Prm{
			LocalServerInfo:      c,
			RemoteWriterProvider: remoteIntermediateTrustProvider,
			Builder:              intermediateRouteBuilder,
		},
		reputationrouter.WithLogger(intermediateTrustLogger),
	)

	eigenTrustCalculator := eigentrustcalc.New(
		eigentrustcalc.Prm{
			AlphaProvider: c.cfgNetmap.wrapper,
			InitialTrustSource: intermediatereputation.InitialTrustSource{
				NetMap: nmSrc,
			},
			IntermediateValueTarget: intermediateTrustRouter,
			WorkerPool:              c.cfgReputation.workerPool,
			FinalResultTarget: intermediatereputation.NewFinalWriterProvider(
				intermediatereputation.FinalWriterProviderPrm{
					PrivatKey: &c.key.PrivateKey,
					PubKey:    localKey,
					Client:    wrap,
				},
				intermediatereputation.FinalWriterWithLogger(c.log),
			),
			DaughterTrustSource: &intermediatereputation.DaughterTrustIteratorProvider{
				DaughterStorage: daughterStorage,
				ConsumerStorage: consumerStorage,
			},
		},
		eigentrustcalc.WithLogger(c.log),
	)

	eigenTrustController := eigentrustctrl.New(
		eigentrustctrl.Prm{
			DaughtersTrustCalculator: &intermediatereputation.DaughtersTrustCalculator{
				Calculator: eigenTrustCalculator,
			},
			IterationsProvider: c.cfgNetmap.wrapper,
			WorkerPool:         c.cfgReputation.workerPool,
		},
		eigentrustctrl.WithLogger(c.log),
	)

	c.cfgReputation.localTrustCtrl = localtrustcontroller.New(
		localtrustcontroller.Prm{
			LocalTrustSource: localTrustStorage,
			LocalTrustTarget: localTrustRouter,
		},
		localtrustcontroller.WithLogger(c.log),
	)

	addNewEpochAsyncNotificationHandler(
		c,
		func(ev event.Event) {
			c.log.Debug("start reporting reputation on new epoch event")

			var reportPrm localtrustcontroller.ReportPrm

			// report collected values from previous epoch
			reportPrm.SetEpoch(ev.(netmap.NewEpoch).EpochNumber() - 1)

			c.cfgReputation.localTrustCtrl.Report(reportPrm)
		},
	)

	server := grpcreputation.New(
		reputationrpc.NewSignService(
			&c.key.PrivateKey,
			reputationrpc.NewResponseService(
				&reputationServer{
					cfg:                c,
					log:                c.log,
					localRouter:        localTrustRouter,
					intermediateRouter: intermediateTrustRouter,
					routeBuilder:       localRouteBuilder,
				},
				c.respSvc,
			),
		),
	)

	for _, srv := range c.cfgGRPC.servers {
		v2reputationgrpc.RegisterReputationServiceServer(srv, server)
	}

	// initialize eigen trust block timer
	newEigenTrustIterTimer(c)

	addNewEpochAsyncNotificationHandler(
		c,
		func(e event.Event) {
			epoch := e.(netmap.NewEpoch).EpochNumber()

			log := c.log.With(zap.Uint64("epoch", epoch))

			duration, err := c.cfgNetmap.wrapper.EpochDuration()
			if err != nil {
				log.Debug("could not fetch epoch duration", zap.Error(err))
				return
			}

			iterations, err := c.cfgNetmap.wrapper.EigenTrustIterations()
			if err != nil {
				log.Debug("could not fetch iteration number", zap.Error(err))
				return
			}

			epochTimer, err := ticker.NewIterationsTicker(duration, iterations, func() {
				eigenTrustController.Continue(
					eigentrustctrl.ContinuePrm{
						Epoch: epoch - 1,
					},
				)
			})
			if err != nil {
				log.Debug("could not create fixed epoch timer", zap.Error(err))
				return
			}

			c.cfgMorph.eigenTrustTicker.addEpochTimer(epoch, epochTimer)
		},
	)
}

type reputationServer struct {
	*cfg
	log                *logger.Logger
	localRouter        reputationcommon.WriterProvider
	intermediateRouter reputationcommon.WriterProvider
	routeBuilder       reputationrouter.Builder
}

func (s *reputationServer) AnnounceLocalTrust(ctx context.Context, req *v2reputation.AnnounceLocalTrustRequest) (*v2reputation.AnnounceLocalTrustResponse, error) {
	passedRoute := reverseRoute(req.GetVerificationHeader())
	passedRoute = append(passedRoute, s)

	body := req.GetBody()

	eCtx := &common.EpochContext{
		Context: ctx,
		E:       body.GetEpoch(),
	}

	w, err := s.localRouter.InitWriter(reputationrouter.NewRouteContext(eCtx, passedRoute))
	if err != nil {
		return nil, fmt.Errorf("could not initialize local trust writer: %w", err)
	}

	for _, trust := range body.GetTrusts() {
		err = s.processLocalTrust(body.GetEpoch(), apiToLocalTrust(&trust, passedRoute[0].PublicKey()), passedRoute, w)
		if err != nil {
			return nil, fmt.Errorf("could not write one of local trusts: %w", err)
		}
	}

	resp := new(v2reputation.AnnounceLocalTrustResponse)
	resp.SetBody(new(v2reputation.AnnounceLocalTrustResponseBody))

	return resp, nil
}

func (s *reputationServer) AnnounceIntermediateResult(ctx context.Context, req *v2reputation.AnnounceIntermediateResultRequest) (*v2reputation.AnnounceIntermediateResultResponse, error) {
	passedRoute := reverseRoute(req.GetVerificationHeader())
	passedRoute = append(passedRoute, s)

	body := req.GetBody()

	eiCtx := eigentrust.NewIterContext(ctx, body.GetEpoch(), body.GetIteration())

	w, err := s.intermediateRouter.InitWriter(reputationrouter.NewRouteContext(eiCtx, passedRoute))
	if err != nil {
		return nil, fmt.Errorf("could not initialize trust writer: %w", err)
	}

	v2Trust := body.GetTrust()

	trust := apiToLocalTrust(v2Trust.GetTrust(), v2Trust.GetTrustingPeer().GetPublicKey())

	err = w.Write(trust)
	if err != nil {
		return nil, fmt.Errorf("could not write trust: %w", err)
	}

	resp := new(v2reputation.AnnounceIntermediateResultResponse)
	resp.SetBody(new(v2reputation.AnnounceIntermediateResultResponseBody))

	return resp, nil
}

func (s *reputationServer) processLocalTrust(epoch uint64, t reputation.Trust,
	passedRoute []reputationcommon.ServerInfo, w reputationcommon.Writer) error {
	err := reputationrouter.CheckRoute(s.routeBuilder, epoch, t, passedRoute)
	if err != nil {
		return fmt.Errorf("wrong route of reputation trust value: %w", err)
	}

	return w.Write(t)
}

// apiToLocalTrust converts v2 Trust to local reputation.Trust, adding trustingPeer.
func apiToLocalTrust(t *v2reputation.Trust, trustingPeer []byte) reputation.Trust {
	var trusted, trusting apireputation.PeerID
	trusted.SetPublicKey(t.GetPeer().GetPublicKey())
	trusting.SetPublicKey(trustingPeer)

	localTrust := reputation.Trust{}

	localTrust.SetValue(reputation.TrustValueFromFloat64(t.GetValue()))
	localTrust.SetPeer(trusted)
	localTrust.SetTrustingPeer(trusting)

	return localTrust
}

func reverseRoute(hdr *session.RequestVerificationHeader) (passedRoute []reputationcommon.ServerInfo) {
	for hdr != nil {
		passedRoute = append(passedRoute, &common.OnlyKeyRemoteServerInfo{
			Key: hdr.GetBodySignature().GetKey(),
		})

		hdr = hdr.GetOrigin()
	}

	return
}
