# Go migration plan

- [ ] Map the Python controller and reviewer contracts to Go packages (GO-1, GO-2).
- [ ] Add Go module, generated protobuf bindings, and red tests for controller behavior.
- [ ] Implement the Go controller, Kubernetes Job submission, and GitHub auth.
- [ ] Implement the Go reviewer, cloning, bridgectl session client, and output posting.
- [ ] Replace Dockerfile, Makefile, scripts, and docs with Go runtime commands (GO-3).
- [ ] Run unit tests, lint, image build, Helm rendering, and the credential-free local smoke path.
- [ ] Record evidence, rollback guidance, and lessons.
