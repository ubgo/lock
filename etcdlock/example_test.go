package etcdlock_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/ubgo/lock/etcdlock"
)

func endpointsFromEnv() []string {
	v := os.Getenv("ETCDLOCK_TEST_ENDPOINTS")
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// Example shows the Acquire / Release flow against an etcd
// cluster. Each Acquire opens a Session (lease + auto-keepalive);
// Release tears it down. If the holder dies, the lease expires
// after the configured TTL and the lock auto-releases.
//
// Run with ETCDLOCK_TEST_ENDPOINTS set to a reachable etcd:
//
//	ETCDLOCK_TEST_ENDPOINTS="localhost:2379" go test
func Example() {
	endpoints := endpointsFromEnv()
	if len(endpoints) == 0 {
		return // Documentation-only when env unset.
	}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 3 * time.Second,
	})
	if err != nil {
		fmt.Println("client:", err)
		return
	}
	defer func() { _ = cli.Close() }()

	locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(30*time.Second))
	ctx := context.Background()

	holder, err := locks.Acquire(ctx, "midnight-billing")
	if err != nil {
		fmt.Println("acquire:", err)
		return
	}
	defer func() { _ = holder.Release() }()

	if _, err := locks.Acquire(ctx, "midnight-billing"); errors.Is(err, etcdlock.ErrLocked) {
		fmt.Println("another holder owns it")
	}
}

// ExampleHolder_Token shows etcd's globally-monotonic
// `mod_revision` used as the fencing token. Useful when
// downstream consumers need a total ordering across multiple
// lock names — etcd revisions are monotonic across the entire
// cluster, not per-name.
func ExampleHolder_Token() {
	endpoints := endpointsFromEnv()
	if len(endpoints) == 0 {
		return
	}

	cli, _ := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 3 * time.Second,
	})
	defer func() { _ = cli.Close() }()

	locks := etcdlock.NewFactory(cli, etcdlock.WithTTL(10*time.Second))
	h, _ := locks.Acquire(context.Background(), "payments-export")
	defer func() { _ = h.Release() }()

	if h.Token() > 0 {
		fmt.Println("globally-monotonic mod_revision assigned")
	}
}
