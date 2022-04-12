package main

import (
	"PandoWatch/pkg/config"
	"PandoWatch/pkg/legs"
	"PandoWatch/pkg/linksystem"
	"PandoWatch/pkg/monitor"
	"context"
	"errors"
	"fmt"
	leveldb "github.com/ipfs/go-ds-leveldb"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/multiformats/go-multiaddr"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/urfave/cli/v2"
	"os"
	"time"
)

const (
	// shutdownTimeout is the duration that a graceful shutdown has to complete
	shutdownTimeout = 5 * time.Second
)

var (
	ErrDaemonStart = errors.New("daemon did not start correctly")
	ErrDaemonStop  = errors.New("daemon did not stop gracefully")
)

var log = logging.Logger("command/pando-provider")

var DaemonCmd = &cli.Command{
	Name:   "daemon",
	Usage:  "Starts a pando provider",
	Flags:  daemonFlags,
	Action: daemonCommand,
}

func daemonCommand(cctx *cli.Context) error {
	err := logging.SetLogLevel("*", cctx.String("log-level"))
	if err != nil {
		return err
	}

	cfg, err := config.Load("")
	if err != nil {
		if err == config.ErrNotInitialized {
			return errors.New("pando provider is not initialized\nTo initialize, run using the \"init\" command")
		}
		return fmt.Errorf("cannot load config file: %w", err)
	}

	_, privKey, err := cfg.Identity.Decode()
	if err != nil {
		return err
	}

	p2pmaddr, err := multiaddr.NewMultiaddr(cfg.ProviderServer.ListenMultiaddr)
	if err != nil {
		return fmt.Errorf("bad p2p address in config %s: %s", cfg.ProviderServer.ListenMultiaddr, err)
	}
	h, err := libp2p.New(
		// Use the keypair generated during init
		libp2p.Identity(privKey),
		// Listen to p2p addr specified in config
		libp2p.ListenAddrs(p2pmaddr),
	)
	if err != nil {
		return err
	}
	log.Infow("libp2p host initialized", "host_id", h.ID(), "multiaddr", p2pmaddr)

	// Initialize datastore
	if cfg.Datastore.Type != "levelds" {
		return fmt.Errorf("only levelds datastore type supported, %q not supported", cfg.Datastore.Type)
	}
	dataStorePath, err := config.Path("", cfg.Datastore.Dir)
	if err != nil {
		return err
	}
	err = checkWritable(dataStorePath)
	if err != nil {
		return err
	}
	ds, err := leveldb.NewDatastore(dataStorePath, nil)
	if err != nil {
		return err
	}
	bstore := blockstore.NewBlockstore(ds)
	linkSystem := linksystem.MkLinkSystem(bstore)

	p, err := peer.Decode("12D3KooWDfuqc2DEUHZq8qUtpbM2o5NF8YZ69dZ8RYkNytAiDbJJ")
	if err != nil {
		return err
	}
	legsProvider, err := legs.New(context.Background(), &legs.PandoInfo{
		PandoMultiAddr: ma.StringCast("/ip4/127.0.0.1/tcp/9002"),
		PandoPeerID:    p,
		Topic:          "/pando/v0.0.1",
	}, h, ds, linkSystem)
	if err != nil {
		return err
	}
	m, err := monitor.New(context.Background(),
		"https://hub.textile.io/thread/bafkwblbznyqkmqx5l677z3kjsslhxo2vbbqh6wluunvvdbmqattrdya/buckets/bafzbeibjg7kky45npdwnogui5ffla7dint62xpttvvlzrsbewlrfmbusya/miner-locations-latest.json",
		linkSystem,
		legsProvider.GetTaskQueue(),
		ds)
	if err != nil {
		return err
	}

	var finalErr error
	select {
	case <-cctx.Done():
	}
	log.Infow("Shutting down daemon")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	go func() {
		// Wait for context to be canceled. If timeout, then exit with error.
		<-shutdownCtx.Done()
		if shutdownCtx.Err() == context.DeadlineExceeded {
			fmt.Println("Timed out on shutdown, terminating...")
			os.Exit(-1)
		}
	}()

	if err = m.Close(); err != nil {
		log.Errorf("Error closing monitor: %s", err)
		finalErr = ErrDaemonStop
	}

	if err = legsProvider.Close(); err != nil {
		log.Errorf("Error closing legsProvider: %s", err)
		finalErr = ErrDaemonStop
	}

	if err = ds.Close(); err != nil {
		log.Errorf("Error closing provider datastore: %s", err)
		finalErr = ErrDaemonStop
	}

	log.Infow("node stopped")
	return finalErr
}