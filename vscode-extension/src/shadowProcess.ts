import * as cp from "child_process";
import * as vscode from "vscode";
import { SessionState, ShadowEvent, SessionInfo } from "./types";

/**
 * Manages a single shadow CLI child process and drives the session state machine.
 *
 * State transitions:
 *   IDLE -> STARTING -> RUNNING_HOST | RUNNING_JOINER -> STOPPING -> IDLE
 *   Any state -> ERROR (on failure)
 */
export class ShadowProcess {
  private proc: cp.ChildProcess | null = null;
  private lineBuf = "";
  private sawStoppedEvent = false;
  private _state: SessionState = SessionState.Idle;
  private _session: SessionInfo | null = null;

  private readonly _onStateChange = new vscode.EventEmitter<SessionState>();
  readonly onStateChange = this._onStateChange.event;

  private readonly _onEvent = new vscode.EventEmitter<ShadowEvent>();
  readonly onEvent = this._onEvent.event;

  private readonly _onSessionUpdate = new vscode.EventEmitter<SessionInfo>();
  readonly onSessionUpdate = this._onSessionUpdate.event;

  private readonly outputChannel: vscode.OutputChannel;

  constructor(outputChannel: vscode.OutputChannel) {
    this.outputChannel = outputChannel;
  }

  get state(): SessionState {
    return this._state;
  }

  get session(): SessionInfo | null {
    return this._session;
  }

  /** Start a host session. */
  start(binaryPath: string, workspacePath: string, options?: { readOnlyJoiners?: boolean }): void {
    if (this._state !== SessionState.Idle && this._state !== SessionState.Error) {
      vscode.window.showWarningMessage("Shadow session already active.");
      return;
    }
    const args = ["start", ".", "--json"];
    const readOnlyJoiners = Boolean(options?.readOnlyJoiners);
    if (readOnlyJoiners) {
      args.push("--read-only-joiners");
    }
    this._session = { mode: "host", workspacePath, hostReadOnly: readOnlyJoiners };
    this.spawn(binaryPath, args, workspacePath);
  }

  /** Join an existing session. */
  join(binaryPath: string, sessionUrl: string, workspacePath: string): void {
    if (this._state !== SessionState.Idle && this._state !== SessionState.Error) {
      vscode.window.showWarningMessage("Shadow session already active.");
      return;
    }
    this._session = { mode: "joiner", joinUrl: sessionUrl, workspacePath };
    this.spawn(binaryPath, ["join", sessionUrl, "--json"], workspacePath);
  }

  /** Gracefully stop the running session. */
  stop(): void {
    if (!this.proc || this._state === SessionState.Idle || this._state === SessionState.Stopping) {
      return;
    }
    this.setState(SessionState.Stopping);

    // SIGINT for graceful shutdown.
    this.proc.kill("SIGINT");

    // Force kill after 5s if still alive.
    const forceTimer = setTimeout(() => {
      if (this.proc) {
        this.proc.kill("SIGKILL");
      }
    }, 5000);

    this.proc.once("exit", () => clearTimeout(forceTimer));
  }

  dispose(): void {
    if (this.proc) {
      this.proc.kill("SIGKILL");
      this.proc = null;
    }
    this._onStateChange.dispose();
    this._onEvent.dispose();
    this._onSessionUpdate.dispose();
  }

  // -------------------------------------------------------------------
  // Private
  // -------------------------------------------------------------------

  private spawn(binaryPath: string, args: string[], cwd: string): void {
    if (this.proc) {
      this.outputChannel.appendLine("[info] stopping previous shadow process before restart");
      this.proc.kill("SIGKILL");
    }
    this.setState(SessionState.Starting);
    this.lineBuf = "";
    this.sawStoppedEvent = false;

    const loggedArgs = args.map((arg, index) => args[0] === "join" && index === 1 ? "<session-url>" : arg);
    this.outputChannel.appendLine(`> ${binaryPath} ${loggedArgs.join(" ")}`);

    const proc = cp.spawn(binaryPath, args, {
      cwd,
      env: { ...process.env, NO_COLOR: "1" },
      stdio: ["ignore", "pipe", "pipe"],
    });
    this.proc = proc;

    proc.stdout?.on("data", (chunk: Buffer) => {
      if (this.proc !== proc) return
      this.onData(chunk)
    });
    proc.stderr?.on("data", (chunk: Buffer) => {
      if (this.proc !== proc) return
      this.outputChannel.appendLine(`[stderr] ${chunk.toString()}`);
    });

    proc.on("error", (err) => {
      if (this.proc !== proc) return
      this.outputChannel.appendLine(`[error] ${err.message}`);
      this.setError(`Process error: ${err.message}`);
    });

    proc.on("exit", (code, signal) => {
      if (this.proc !== proc) return
      this.outputChannel.appendLine(`[exit] code=${code} signal=${signal}`);
      this.proc = null;
      if (this._state === SessionState.Stopping || this.sawStoppedEvent) {
        this.setState(SessionState.Idle);
        this._session = null;
        this.sawStoppedEvent = false;
      } else if (this._state !== SessionState.Idle && this._state !== SessionState.Error) {
        this.setError(`Process exited unexpectedly (code ${code})`);
      }
    });
  }

  private onData(chunk: Buffer): void {
    this.lineBuf += chunk.toString();
    const lines = this.lineBuf.split("\n");
    // Keep the last (possibly incomplete) segment in the buffer.
    this.lineBuf = lines.pop() ?? "";

    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      this.parseLine(trimmed);
    }
  }

  private parseLine(line: string): void {
    let evt: ShadowEvent;
    try {
      evt = JSON.parse(line) as ShadowEvent;
    } catch {
      // Non-JSON line — log and ignore.
      this.outputChannel.appendLine(`[stdout] ${line}`);
      return;
    }

    this.outputChannel.appendLine(`[event] ${evt.event}: ${evt.message}`);
    this._onEvent.fire(evt);

    switch (evt.event) {
      case "starting":
        // Already in Starting state from spawn().
        break;

      case "tunnel_ready":
        if (this._session) {
          this._session.joinUrl = evt.join_url;
          this._session.joinCommand = evt.join_command;
          this._onSessionUpdate.fire(this._session);
        }
        this.setState(SessionState.RunningHost);
        break;

      case "connected":
        this.setState(SessionState.RunningJoiner);
        break;

      case "snapshot_complete":
        if (this._session) {
          this._session.fileCount = evt.file_count;
          this._onSessionUpdate.fire(this._session);
        }
        break;

      case "read_only":
        if (this._session) {
          this._session.readOnly = true;
          this._onSessionUpdate.fire(this._session);
        }
        break;

      case "error":
        this.setError(evt.message);
        break;

      case "disconnected":
        this.setError("Disconnected");
        break;

      case "warning":
        vscode.window.showWarningMessage(`Shadow: ${evt.message}`);
        break;

      case "stopped":
        this.sawStoppedEvent = true;
        if (this._state !== SessionState.Stopping) {
          this.setState(SessionState.Stopping);
        }
        break;

      // file_sent, file_received — no state change, just logged.
    }
  }

  private setState(state: SessionState): void {
    if (this._state === state) return;
    this._state = state;
    this._onStateChange.fire(state);
  }

  private setError(message: string): void {
    if (this._session) {
      this._session.lastError = message;
      this._onSessionUpdate.fire(this._session);
    }
    this.setState(SessionState.Error);
  }
}
