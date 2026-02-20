# Shadow

Real-time code collaboration that works with any editor. Like Google Docs, but for coding.

<video src="demo.mp4"
       width="720"
       muted
       autoplay
       loop
       playsinline
       controls></video>


## Quick Start

**Install:**

```bash
curl -sSf https://raw.githubusercontent.com/go-johnnyhe/shadow/main/install.sh | sh
```

**Interactive flow (recommended):**

```bash
shadow
```

Then choose `Start` or `Join` in the prompt.

**Direct commands (still supported):**

```bash
shadow start .
shadow join '<session-url>#<key>'
```

## How it works

1. Run `shadow` (or `shadow start filename.py`) in your project
2. Share the generated URL 
3. Your partner runs `shadow join '<url>#<key>'`
4. Both see live changes with `→` and `←` indicators

### Optional start flags

- `--read-only-joiners`: joiners receive updates but cannot upload local edits
- `--key <secret>`: set your own E2E key (otherwise Shadow auto-generates one)
- `--path <path>`: pass share path as a flag instead of positional argument
- `--force`: bypass the large-directory safety prompt

Security note: file payloads are E2E encrypted between clients. The generated join link includes `#<key>` so the receiver can decrypt.

Works with Vim, Neovim, VS Code, JetBrains, or any editor.

## Why Shadow?

Screensharing is clunky. Git is too slow for real-time work. Live Share only works in VS Code.

Shadow syncs files directly—use whatever editor you want.

## Use Cases

- Mock interviews
- Pair programming
- Debug sessions  
- Code reviews

## Limitations

- Repos >100 MB not optimized yet
- Last write wins (no merge conflicts)

---

Built with Go + WebSockets + Cloudflared. Open source.
