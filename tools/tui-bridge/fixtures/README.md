# TUI bridge fixtures

These fixtures drive the browser/xterm demo through the real Kitsoki TUI process
served by `kitsoki tui-serve`. They are deterministic: free-text routing is
replayed from `dogfood-marathon-recording.yaml`, and LLM-bearing `host.agent.*`
calls are replayed from `dogfood-marathon.host.cassette.yaml`.

The bug markdown files mirror live issue identities and URLs for demo realism,
but the cassette outcomes are authored replay data. They are not a claim that a
paid live GPT-5.5 marathon fixed those issues.
