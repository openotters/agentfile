// Package build creates OCI artifacts from parsed Agentfiles.
//
// Build reads an Agentfile and a source filesystem, then pushes layers, a config
// blob, and a manifest into any oras.Target (memory store, remote registry, etc.).
//
//	Agentfile + src filesystem
//	        |
//	        v
//	  +-----------+
//	  |   Build   |
//	  +-----------+
//	        |
//	        v
//	  oras.Target (dst)
//	  +---------------------------------------------+
//	  | manifest (tagged "latest")                   |
//	  |   artifactType: .../agent.v1                 |
//	  |   config: .../agent.config.v1+json           |
//	  |     +--------------------------------------+ |
//	  |     | full Agentfile JSON (source of truth)| |
//	  |     +--------------------------------------+ |
//	  |   layers:                                    |
//	  |     [0] SOUL.md     (.../context.v1)         |
//	  |     [1] IDENTITY.md (.../context.v1)         |
//	  |     [2] /data/cities.json (octet-stream)     |
//	  |   annotations:                               |
//	  |     from LABEL + NAME                        |
//	  +---------------------------------------------+
//
// The dst can then be copied to a remote registry with oras.Copy, exported to
// JSON with the export package, or loaded back into an Agentfile with pkg.Load.
package build
