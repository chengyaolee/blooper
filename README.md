# blooper
Making agents work more with less

## Browser tool setup

The `browser` tool drives Chrome through [Scrapling](https://github.com/D4Vinci/Scrapling)'s MCP server (stealth fingerprints, Cloudflare solver).

```sh
python3.12 -m venv ~/.venvs/scrapling      # any Python 3.10+
~/.venvs/scrapling/bin/pip install "scrapling[ai]"
~/.venvs/scrapling/bin/scrapling install   # downloads Chromium (+ system libs when run as root)
export SCRAPLING_MCP_BIN=~/.venvs/scrapling/bin/scrapling-mcp   # or put scrapling-mcp on PATH
```

- **Desktop:** if Google Chrome is installed it is used, otherwise Scrapling's bundled Chromium.
- **Headless Linux server (no `DISPLAY`):** also `apt install xvfb`. blooper then runs Chrome on its own virtual display, which Cloudflare Turnstile tolerates better than `--headless`. Without Xvfb it falls back to plain headless (pages load, but Turnstile pages may not solve). In Docker, give the container `--shm-size=256m` or more.
- If `scrapling-mcp` is missing the tool returns an install hint, and `TestBrowserDetection` is skipped.
