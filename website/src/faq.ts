// Answers shared by the FAQ section and its FAQPage schema. Backticks mark code.
export const faqs = [
  {
    q: 'Does my partner need the same editor as me?',
    a: 'No. Shadow syncs the files on disk, so each of you can use VS Code, Neovim, Vim, JetBrains, Zed, or anything else that reads and saves files.',
  },
  {
    q: 'Can the relay server read my code?',
    a: 'No. Files are encrypted with `AES-256-GCM` before they leave your machine. The key lives in the invite link after the #, a part of the URL that is never sent to a server, so the relay only ever handles ciphertext.',
  },
  {
    q: 'Do we need accounts or a sign in?',
    a: 'No. There is no email, OAuth, or workspace to set up. The host starts a session, Shadow copies the invite link, and whoever has that link can join.',
  },
  {
    q: 'What happens if we both edit the same file at once?',
    a: 'The last write wins. Shadow does not merge conflicting edits, so it works best when you talk about who is editing what, the way you would at one desk.',
  },
  {
    q: 'Can someone watch without being able to edit?',
    a: 'Yes. Turn on "Joiners are read only" in the Mac app, choose Start Read Only Session in VS Code, or run `shadow start --read-only-joiners`. That suits interviews and code reviews.',
  },
  {
    q: 'Is Shadow only for Mac?',
    a: 'The menu bar app needs macOS 13 or later. The CLI runs on macOS and Linux through the install script, and through `go install` if you have Go. The VS Code extension uses the CLI underneath.',
  },
  {
    q: 'How large a project can I share?',
    a: 'Shadow is built for project sized folders. Repositories over about 100 MB may feel slow. It warns you before sharing more than 500 files or 50 MB, respects `.gitignore`, and skips `.git`, `node_modules`, and swap files.',
  },
  {
    q: 'Do I need to open ports or configure a firewall?',
    a: 'No. Shadow creates a public HTTPS endpoint through a Cloudflare tunnel. The tunnel helper downloads automatically to `~/.shadow` on first run.',
  },
  {
    q: 'Does Vim reload files when my partner saves?',
    a: 'Yes, once it is set up. The install script configures Vim and Neovim to reload changed buffers automatically. If you installed another way, run `shadow vimSetup`.',
  },
  {
    q: 'What does it cost?',
    a: 'Nothing. Shadow is free and open source under the MIT license.',
  },
];

export const plainAnswer = (a: string) => a.replace(/`/g, '');
