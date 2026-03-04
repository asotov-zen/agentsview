<script lang="ts">
  import type { Session } from "../../api/types.js";
  import { copyToClipboard } from "../../utils/clipboard.js";
  import { agentColor } from "../../utils/agents.js";

  interface Props {
    session: Session | undefined;
    onBack: () => void;
  }

  let { session, onBack }: Props = $props();
  let copiedSessionId = $state("");
  let showInfoPanel = $state(false);
  let copiedField = $state("");

  function sessionDisplayId(id: string): string {
    const idx = id.indexOf(":");
    return idx >= 0 ? id.slice(idx + 1) : id;
  }

  function toggleInfoPanel() {
    showInfoPanel = !showInfoPanel;
  }

  async function copyField(value: string, field: string) {
    const ok = await copyToClipboard(value);
    if (!ok) return;
    copiedField = field;
    setTimeout(() => {
      if (copiedField === field) copiedField = "";
    }, 1500);
  }

  async function copySessionId(rawId: string, sessionId: string) {
    const ok = await copyToClipboard(rawId);
    if (!ok) return;

    copiedSessionId = sessionId;
    setTimeout(() => {
      if (copiedSessionId === sessionId) copiedSessionId = "";
    }, 1500);
  }

  const isClaudeAgent = $derived(
    session?.agent === "claude",
  );

  const resumeCommand = $derived.by(() => {
    if (!session || !isClaudeAgent) return "";
    const rawId = sessionDisplayId(session.id);
    const cwd = session.cwd;
    if (cwd) {
      return `cd ${cwd} && claude --resume ${rawId}`;
    }
    return `claude --resume ${rawId}`;
  });
</script>

<div class="session-breadcrumb-wrapper">
  <div class="session-breadcrumb">
    <button class="breadcrumb-link" onclick={onBack}>Sessions</button>
    <span class="breadcrumb-sep">/</span>
    <span class="breadcrumb-current">{session?.project ?? ""}</span>
    {#if session}
      <span class="breadcrumb-meta">
        <span
          class="agent-badge"
          style:background={agentColor(session.agent)}
        >{session.agent}</span>
        {#if session.started_at}
          <span class="session-time">
            {new Date(session.started_at).toLocaleDateString(undefined, {
              month: "short",
              day: "numeric",
            })}
            {new Date(session.started_at).toLocaleTimeString(undefined, {
              hour: "2-digit",
              minute: "2-digit",
            })}
          </span>
        {/if}
        {#if session.id}
          {@const rawId = sessionDisplayId(session.id)}
          <button
            class="session-id"
            class:active={showInfoPanel}
            title="Session info"
            onclick={toggleInfoPanel}
          >
            {copiedSessionId === session.id ? "Copied!" : rawId.slice(0, 8)}
            <svg class="info-chevron" class:open={showInfoPanel} width="8" height="8" viewBox="0 0 8 8" fill="currentColor">
              <path d="M2 3l2 2 2-2"/>
            </svg>
          </button>
        {/if}
      </span>
    {/if}
  </div>

  {#if showInfoPanel && session}
    <div class="info-panel">
      <div class="info-row">
        <span class="info-label">Session ID</span>
        <code class="info-value">{sessionDisplayId(session.id)}</code>
        <button
          class="info-copy"
          onclick={() => copyField(sessionDisplayId(session.id), "id")}
        >{copiedField === "id" ? "Copied" : "Copy"}</button>
      </div>
      {#if session.cwd}
        <div class="info-row">
          <span class="info-label">Path</span>
          <code class="info-value">{session.cwd}</code>
          <button
            class="info-copy"
            onclick={() => copyField(session.cwd!, "cwd")}
          >{copiedField === "cwd" ? "Copied" : "Copy"}</button>
        </div>
      {/if}
      {#if isClaudeAgent && resumeCommand}
        <div class="info-row">
          <span class="info-label">Resume</span>
          <code class="info-value">{resumeCommand}</code>
          <button
            class="info-copy"
            onclick={() => copyField(resumeCommand, "resume")}
          >{copiedField === "resume" ? "Copied" : "Copy"}</button>
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .session-breadcrumb-wrapper {
    flex-shrink: 0;
  }

  .session-breadcrumb {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 32px;
    padding: 0 14px;
    border-bottom: 1px solid var(--border-muted);
    flex-shrink: 0;
    font-size: 11px;
    color: var(--text-muted);
  }

  .breadcrumb-link {
    color: var(--text-muted);
    font-size: 11px;
    font-weight: 500;
    cursor: pointer;
    transition: color 0.12s;
  }

  .breadcrumb-link:hover {
    color: var(--accent-blue);
  }

  .breadcrumb-sep {
    opacity: 0.3;
    font-size: 10px;
  }

  .breadcrumb-current {
    color: var(--text-primary);
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    flex: 1;
    min-width: 0;
  }

  .breadcrumb-meta {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-left: auto;
    flex-shrink: 0;
  }

  .agent-badge {
    font-size: 9px;
    font-weight: 600;
    padding: 1px 6px;
    border-radius: 8px;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    color: white;
    flex-shrink: 0;
    background: var(--text-muted);
  }

  .session-time {
    font-size: 10px;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .session-id {
    font-size: 10px;
    font-family: "SF Mono", "Menlo", "Consolas", monospace;
    color: var(--text-muted);
    cursor: pointer;
    padding: 1px 5px;
    border-radius: 4px;
    background: var(--bg-tertiary);
    transition: color 0.15s, background 0.15s;
    white-space: nowrap;
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }

  .session-id:hover {
    color: var(--text-secondary);
    background: var(--bg-hover);
  }

  .session-id.active {
    color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
  }

  .info-chevron {
    transition: transform 0.15s;
  }

  .info-chevron.open {
    transform: rotate(180deg);
  }

  .info-panel {
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-inset);
    padding: 8px 14px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .info-row {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 22px;
  }

  .info-label {
    font-size: 10px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
    width: 70px;
    flex-shrink: 0;
  }

  .info-value {
    font-size: 11px;
    font-family: "SF Mono", "Menlo", "Consolas", monospace;
    color: var(--text-secondary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    flex: 1;
    min-width: 0;
  }

  .info-copy {
    font-size: 10px;
    color: var(--text-muted);
    padding: 1px 6px;
    border-radius: 3px;
    cursor: pointer;
    flex-shrink: 0;
    transition: color 0.12s, background 0.12s;
  }

  .info-copy:hover {
    color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 8%, transparent);
  }
</style>
