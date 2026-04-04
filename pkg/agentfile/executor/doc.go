// Package executor defines the interface for materializing and running an agent
// from a built Agentfile artifact.
//
// An Executor takes a BuildResult and produces a running agent environment:
// filesystem layout, tool binaries, context files, and data files — everything
// the agent runtime needs to start.
//
// Implementations:
//   - FileExecutor: materializes the agent as a directory tree on the local
//     filesystem following the FHS layout defined in the Agentfile spec.
//
// Future implementations may target Docker, Kubernetes, or other runtimes.
package executor
