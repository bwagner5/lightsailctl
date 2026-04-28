package tests

import "github.com/aws/lightsailctl/pkg/lsctltest/testkit"

func init() {
	testkit.Register("instance-lifecycle", instanceLifecycle)
}

func instanceLifecycle(t *testkit.T) {
	name := t.NewName("inst")

	t.CreateInstance(name)
	t.WaitForInstanceRunning(name)

	if t.Env().Keep {
		t.Logf("--keep: leaving instance %s in place", name)
		return
	}
	t.DeleteInstance(name)
	t.AssertInstanceGone(name)
}
