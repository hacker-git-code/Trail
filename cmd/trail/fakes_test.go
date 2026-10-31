package main

// Fake credentials for tests, built from pieces so that no complete
// key-shaped string appears in the source (secret scanners would flag it).
var (
	fakeAnthropic = "sk-" + "ant-" + "api03-" + "Zk3n9QpLmX7vT2wR8yB4cD6eF1gH5jK0lM"
	fakeOpenAI    = "sk-" + "proj-" + "9fJ2kL8mN4pQ6rS1tU3vW5xY7zA0bC2dE4fG"
	fakeStripe    = "sk_" + "live_" + "51HxQ9a2bC3dE4fG5hI6jK7lM"
	fakeGitHub    = "gh" + "p_" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"
	fakeAWS       = "AKIA" + "IOSFODNN7" + "EXAMPLE"
)
