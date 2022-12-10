package gkehub

import (
	"context"
	"flag"
	"time"

	gkehub "cloud.google.com/go/gkehub/apiv1beta1"
	gkehubpb "cloud.google.com/go/gkehub/apiv1beta1/gkehubpb"
	"github.com/go-logr/logr"
	"google.golang.org/api/iterator"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Options struct {
}

func (o *Options) InitDefaults() {
}

func (o *Options) BindFlags(prefix string, flags *flag.FlagSet) {
}

func NewGKEHubRunner() *GKEHubRunner {
	return &GKEHubRunner{}
}

type GKEHubRunner struct {
	Options

	client.Client

	logger logr.Logger
}

func (g *GKEHubRunner) Start(ctx context.Context) error {
	g.logger.Info("Starting GKEHubRunner")
	go g.doWork(ctx)
	<-ctx.Done()
	return ctx.Err()
}

func (g *GKEHubRunner) doWork(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.sync(ctx)
		}
	}
}

func (g *GKEHubRunner) sync(ctx context.Context) {
	c, err := gkehub.NewGkeHubMembershipClient(ctx)
	if err != nil {
		g.logger.Error(err, "error creating GKEHubMembershipClient")
		return
	}

	defer c.Close()

	req := &gkehubpb.ListMembershipsRequest{
		Parent: "projects/mortent-kube-dev/locations/global",
	}
	it := c.ListMemberships(ctx, req)
	for {
		resp, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			g.logger.Error(err, "error iterating over memberships")
		}
		g.logger.Info("membership", "result", resp)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (g *GKEHubRunner) SetupWithManager(mgr ctrl.Manager) error {
	g.Client = mgr.GetClient()

	return mgr.Add(g)
}

func (g *GKEHubRunner) InjectLogger(l logr.Logger) error {
	g.logger = l
	return nil
}
