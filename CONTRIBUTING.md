# Contributing

Thanks for taking a look at YaomuStudio. This repository is the public mirror of an actively developed desktop client; the core team reviews changes before they land.

## Before you start

- Open an issue for a bug or a feature before sending a large pull request.
- Keep a pull request focused on one topic. Avoid unrelated refactors, formatting changes, or dependency upgrades.
- If a change touches `contracts/` or a cross-process schema, update the contract first and explain the migration impact.

## Development

Requirements: Node 22, Go 1.23+, Python 3.11+.

```bash
./scripts/setup-dev.sh
npm install
npm run typecheck
npm run build
```

Tests:

```bash
npm run test:macos   # local macOS automation baseline
npm run test:e2e     # UI E2E with browser fixtures
cd app-server && go test ./...
.venv/bin/python -m pytest runner/tests -q
```

Desktop changes need to be checked in a real Electron run; a successful compile is not enough.

## Pull requests

- Describe the user-visible behavior and the verification you ran.
- Include a focused test when the change fixes a bug or adds a contract.
- Do not include credentials, local absolute paths, model weights, or generated build output.
- Platform differences matter: model execution is Windows + WSL2 first; macOS/Linux media support is a preview path.

## License

By contributing, you agree that your contribution is licensed under Apache-2.0.
