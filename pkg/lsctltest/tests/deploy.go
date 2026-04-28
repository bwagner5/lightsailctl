package tests

import "github.com/aws/lightsailctl/pkg/lsctltest/testkit"

func init() {
	testkit.Register("deploy-hello", deployHello)
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
