package tests

import (
	"fmt"
	"path/filepath"

	"github.com/aws/lightsailctl/pkg/lsctltest/testkit"
)

func init() {
	testkit.Register("deploy-hello", deployHello)
	testkit.Register("deploy-hello-explicit-existing", deployHelloExplicitExisting)
}

func deployHello(t *testkit.T) {
	const (
		appDir = "test/data/hello"
		appEnv = "test"
	)
	instance := t.NewName("deploy")
	appName := t.NewName("hello")

	t.CreateInstance(instance)
	t.WaitForInstanceRunning(instance)

	// First deploy of a fresh repo bootstraps the app (creates buckets,
	// tags instance, installs agent) before rolling out the asset.
	t.DeployApp(appName, appEnv, instance, appDir)

	endpoints := t.AppEndpoints(appName, appEnv)
	t.AssertEndpoints2xx(endpoints)

	if t.Env().Keep {
		t.Logf("--keep: leaving app %s and instance %s in place", appName, instance)
		return
	}
	t.DeleteApp(appName)
	t.DeleteInstance(instance)
}

// deployHelloExplicitExisting exercises the new in-deploy "pick instance
// strategy" branch: --create-new-instance=false with --instance=<existing>
// must short-circuit through pickInstanceStrategyStep to the use-existing
// path (no yes/no prompt, no new instance created). This guards against
// regressions in the step ordering: if pickInstanceStrategyStep or
// pickExistingInstanceStep ever paused the saga under -y, the deploy
// would abort with a "needs input" error and this test would fail.
//
// The create-new branch itself (`--create-new-instance=true`) is not
// reachable under -y because its instance-create inputs flow through
// NeedInput (namespaced "__ni/*" that aren't CLI flags); that path is
// covered by unit tests + manual interactive verification.
func deployHelloExplicitExisting(t *testkit.T) {
	const (
		appDir = "test/data/hello"
		appEnv = "test"
	)
	instance := t.NewName("deploy")
	appName := t.NewName("hello")

	t.CreateInstance(instance)
	t.WaitForInstanceRunning(instance)

	agentPath := t.Env().BinaryAgent
	if abs, err := filepath.Abs(agentPath); err == nil {
		agentPath = abs
	}
	// Explicit --create-new-instance=false + --instance=<existing>:
	// exercise pickInstanceStrategyStep's "use-existing" short-circuit
	// and pickExistingInstanceStep's immediate return (instance set).
	t.RunCLIInDir(fmt.Sprintf("deploy app %s (explicit existing)", appName), appDir,
		"deploy",
		"--name", appName,
		"--env", appEnv,
		"--instance", instance,
		"--create-new-instance", "false",
		"--agent-path", agentPath,
		"--region", t.Env().Region,
		"--wait-timeout", "2m",
	)

	endpoints := t.AppEndpoints(appName, appEnv)
	t.AssertEndpoints2xx(endpoints)

	// Sanity-check: the deploy step log should NOT have paused for
	// "needs input" (the strategy step short-circuits). We don't have a
	// step-log inspector here; RunCLIInDir would have failed on a
	// "needs input" exit, so reaching this point is the assertion.

	if t.Env().Keep {
		t.Logf("--keep: leaving app %s and instance %s in place", appName, instance)
		return
	}
	t.DeleteApp(appName)
	t.DeleteInstance(instance)
}
