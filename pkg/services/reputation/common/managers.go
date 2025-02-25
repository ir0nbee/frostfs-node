package common

import (
	"fmt"

	netmapcore "github.com/TrueCloudLab/frostfs-node/pkg/core/netmap"
	"github.com/TrueCloudLab/frostfs-node/pkg/util/logger"
	apiNetmap "github.com/TrueCloudLab/frostfs-sdk-go/netmap"
	apireputation "github.com/TrueCloudLab/frostfs-sdk-go/reputation"
	"github.com/TrueCloudLab/hrw"
	"go.uber.org/zap"
)

// managerBuilder is implementation of reputation ManagerBuilder interface.
// It sorts nodes in NetMap with HRW algorithms and
// takes the next node after the current one as the only manager.
type managerBuilder struct {
	log   *logger.Logger
	nmSrc netmapcore.Source
	opts  *mngOptions
}

// ManagersPrm groups the required parameters of the managerBuilder's constructor.
//
// All values must comply with the requirements imposed on them.
// Passing incorrect parameter values will result in constructor
// failure (error or panic depending on the implementation).
type ManagersPrm struct {
	NetMapSource netmapcore.Source
}

// NewManagerBuilder creates a new instance of the managerBuilder.
//
// Panics if at least one value of the parameters is invalid.
//
// The created managerBuilder does not require additional
// initialization and is completely ready for work.
func NewManagerBuilder(prm ManagersPrm, opts ...MngOption) ManagerBuilder {
	switch {
	case prm.NetMapSource == nil:
		panic(fmt.Sprintf("invalid NetMapSource (%T):%v", prm.NetMapSource, prm.NetMapSource))
	}

	o := defaultMngOpts()

	for i := range opts {
		opts[i](o)
	}

	return &managerBuilder{
		log:   o.log,
		nmSrc: prm.NetMapSource,
		opts:  o,
	}
}

// implements Server on apiNetmap.NodeInfo.
type nodeServer apiNetmap.NodeInfo

func (x nodeServer) PublicKey() []byte {
	return (apiNetmap.NodeInfo)(x).PublicKey()
}

func (x nodeServer) IterateAddresses(f func(string) bool) {
	(apiNetmap.NodeInfo)(x).IterateNetworkEndpoints(f)
}

func (x nodeServer) NumberOfAddresses() int {
	return (apiNetmap.NodeInfo)(x).NumberOfNetworkEndpoints()
}

func (x nodeServer) ExternalAddresses() []string {
	return (apiNetmap.NodeInfo)(x).ExternalAddresses()
}

// BuildManagers sorts nodes in NetMap with HRW algorithms and
// takes the next node after the current one as the only manager.
func (mb *managerBuilder) BuildManagers(epoch uint64, p apireputation.PeerID) ([]ServerInfo, error) {
	mb.log.Debug("start building managers",
		zap.Uint64("epoch", epoch),
		zap.Stringer("peer", p),
	)

	nm, err := mb.nmSrc.GetNetMapByEpoch(epoch)
	if err != nil {
		return nil, err
	}

	nmNodes := nm.Nodes()

	// make a copy to keep order consistency of the origin netmap after sorting
	nodes := make([]apiNetmap.NodeInfo, len(nmNodes))

	copy(nodes, nmNodes)

	hrw.SortHasherSliceByValue(nodes, epoch)

	for i := range nodes {
		if apireputation.ComparePeerKey(p, nodes[i].PublicKey()) {
			managerIndex := i + 1

			if managerIndex == len(nodes) {
				managerIndex = 0
			}

			return []ServerInfo{nodeServer(nodes[managerIndex])}, nil
		}
	}

	return nil, nil
}

type mngOptions struct {
	log *logger.Logger
}

type MngOption func(*mngOptions)

func defaultMngOpts() *mngOptions {
	return &mngOptions{
		log: &logger.Logger{Logger: zap.L()},
	}
}

// WithLogger returns MngOption to specify logging component.
func WithLogger(l *logger.Logger) MngOption {
	return func(o *mngOptions) {
		if l != nil {
			o.log = l
		}
	}
}
