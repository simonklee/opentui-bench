# AGENTS.md

AI coding help for working with this repository.

**opentui-bench** tracks Zig benchmark performance over time for the [opentui
project](https://github.com/anomalyco/opentui).

- Go web server with SQLite database
- CLI for recording/viewing benchmark results
- Solid.js web UI for visualizing trends

## Development Commands

```bash
make dev             # Start development server (auto-reloads, logs to dev.log)
make display-log     # Display the last 100 lines of dev.log

## Other commands:
make build           # Build the binary (not required for dev)
make test            # Run all tests
make serve           # Run the server directly (not required for dev)
make help            # Show all available commands
```

**IMPORTANT:**

- `make dev` is all you need - it auto-rebuilds on file changes.
- Server logs to `dev.log`. Use `make display-log` to read it.
- NEVER stop the server! It auto-reloads on changes.
- Use **localhost:3000** for frontend development (HMR, instant updates).
- Port 8080 serves embedded static files and only updates after `make build`.

## CLI Commands

```bash
./bench list                         # List recorded runs
./bench show <commit>                # Show run details
./bench compare <commit1> <commit2>  # Compare two runs
./bench trend "benchmark_name"       # Show performance trend
```

## Architecture

- `cmd/bench/` - CLI entry point
- `internal/db/` - SQLite database layer
- `internal/web/` - HTTP handlers and embedded static files
- `internal/runner/` - Benchmark execution and recording
- `internal/record/` - Benchmark result parser and data structures

## Using Playwright (playwriter tool)

The `playwriter_execute` tool controls the user's Chrome browser for UI testing and
exploration. The frontend runs at **localhost:3000**.

### What Works Well

- **Direct navigation with `page.goto()`** - Most reliable way to navigate
- **`accessibilitySnapshot()`** - Fast way to read page structure and find elements
- **`aria-ref` selectors** - Use refs from snapshots: `page.locator('aria-ref=e25')`
- **Form interactions** - `selectOption()`, `fill()`, `click()` work reliably
- **Evaluating page content** - `page.locator().allTextContents()` for extracting data

### What Doesn't Work Well

- **`screenshotWithAccessibilityLabels()`** - Often times out, even with 20s timeout
- **Clicking navigation links** - Can timeout; use `page.goto()` directly instead
- **`waitForLoadState('networkidle')`** - Unreliable with dev servers using HMR
- **Complex chained operations** - Split into multiple smaller execute calls

### Effective Patterns

```js
// Navigate directly (preferred over clicking links)
await page.goto("http://localhost:3000/compare", {
  waitUntil: "domcontentloaded",
});

// Get page structure
console.log(await accessibilitySnapshot({ page }));

// Search for specific elements
const snapshot = await accessibilitySnapshot({
  page,
  search: /button|submit/i,
});

// Interact using aria-ref (no quotes around ref value)
await page.locator("aria-ref=e49").selectOption({ index: 2 });

// Extract table data
const rows = await page.locator("table tbody tr").all();
for (const row of rows) {
  const cells = await row.locator("td").allTextContents();
  console.log(cells);
}

// Paginate long snapshots
console.log(snapshot.split("\n").slice(0, 50).join("\n")); // first 50 lines
console.log(snapshot.split("\n").slice(50, 100).join("\n")); // next 50 lines
```

### Troubleshooting

- **Timeout errors**: Use shorter operations, increase timeout to 10000-20000ms
- **Element not found**: Re-fetch `accessibilitySnapshot()` as refs change after navigation
- **Page not responding**: Use `playwriter_reset` tool to reconnect
- **Strict mode violation**: Use `.first()`, `.last()`, or `.nth(n)` for multiple matches
