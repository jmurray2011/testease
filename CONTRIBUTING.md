# Contributing

Run the full gate before opening a change:

```text
make validate
make build
```

Behavior changes need tests. For scenario and campaign changes, include a
fixture that shows the smallest useful YAML. Keep provider-specific tests
behind their existing environment checks; do not make the default test suite
depend on a private machine.

Do not commit run records, `.tools/`, credentials, or input bundles.
