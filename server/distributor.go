package server

import (
	"net"
	"sync"

	"google.golang.org/grpc"

	"github.com/coreos/agro"
	"github.com/coreos/agro/models"
	"github.com/dgryski/go-tinylfu"
)

const (
	defaultINodeReplication = 3
)

type distributor struct {
	mut       sync.RWMutex
	blocks    agro.BlockStore
	srv       *server
	client    *distClient
	grpcSrv   *grpc.Server
	readCache *tinylfu.T

	ring           agro.Ring
	closed         bool
	rebalancerChan chan struct{}
	rebalancer     Rebalancer
}

func newDistributor(srv *server, addr string, listen bool) (*distributor, error) {
	var err error
	d := &distributor{
		blocks: srv.blocks,
		srv:    srv,
	}
	if listen {
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		d.grpcSrv = grpc.NewServer()
		models.RegisterAgroStorageServer(d.grpcSrv, d)
		go d.grpcSrv.Serve(lis)
		srv.BeginHeartbeat()
	}
	gmd, err := d.srv.mds.GlobalMetadata()
	if err != nil {
		return nil, err
	}
	if srv.cfg.ReadCacheSize != 0 {
		size := srv.cfg.ReadCacheSize / gmd.BlockSize
		if size < 100 {
			size = 100
		}
		d.readCache = tinylfu.New(int(size), 10*int(size))
	}

	// Set up the rebalancer
	d.ring, err = d.srv.mds.GetRing()
	if err != nil {
		return nil, err
	}
	d.rebalancerChan = make(chan struct{})
	go d.rebalanceWatcher(d.rebalancerChan)
	d.client = newDistClient(d)
	return d, nil
}

func (d *distributor) UUID() string {
	return d.srv.mds.UUID()
}

func (d *distributor) Close() error {
	d.mut.Lock()
	defer d.mut.Unlock()
	if d.closed {
		return nil
	}
	close(d.rebalancerChan)
	d.grpcSrv.Stop()
	d.client.Close()
	err := d.blocks.Close()
	if err != nil {
		return err
	}
	d.closed = true
	return nil
}
