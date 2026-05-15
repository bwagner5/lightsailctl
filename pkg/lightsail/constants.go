// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

// Naming prefixes and on-instance paths. See plan.md §8.
const (
	// BucketPrefix is prepended to every application/environment bucket.
	// Shape:
	//   app-config bucket: ls--<account>--<app>
	//   env bucket:        ls--<account>--<app>--<env>
	BucketPrefix = "ls--"

	// TagPrefix is prepended to instance tag keys that mark an instance
	// as a deployment target: ls:app:<app>:<env> = "true".
	TagPrefix = "ls:app:"

	// BaseDir is the root of on-instance state: /opt/lightsail/<app>/<env>.
	BaseDir = "/opt/lightsail"

	// UnitNameFmt is the systemd watcher unit: lightsail-watch-<app>-<env>.service.
	UnitNameFmt = "lightsail-watch-%s-%s"

	// DefaultBundle is the small bucket bundle used when we create buckets.
	DefaultBundle = "small_1_0"

	// DeployPrefix is the S3 key prefix for deploy tarballs inside an env bucket.
	DeployPrefix = "deploy/"

	// StatusSuffix is appended to <instance> for the status file key:
	// <instance>_status.json.
	StatusSuffix = "_status.json"
)
