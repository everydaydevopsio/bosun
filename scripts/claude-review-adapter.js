// Keep Claude's final answer while surfacing permission failures that its text
// output hides. The provider's raw event stream can contain prompts, source
// code, and credentials, so never forward it to bridgectl or Job logs.
'use strict';

const { spawn } = require('node:child_process');
const readline = require('node:readline');
const { claudeUsage, safeReview, usageLine, zeroedExecutionFailure } = require('./provider-usage');

const claude = '/app/node_modules/@anthropic-ai/claude-code/bin/claude.exe';
const allowed = [
  'Read', 'Glob', 'Grep',
  'Bash(git diff)', 'Bash(git diff *)',
  'Bash(git show)', 'Bash(git show *)',
  'Bash(git log)', 'Bash(git log *)',
  'Bash(git status)', 'Bash(git status *)',
  'Bash(git ls-files)', 'Bash(git ls-files *)',
  'Bash(git rev-parse *)', 'Bash(git merge-base *)',
  'Bash(git rev-list *)', 'Bash(git cat-file -p *)',
  'Bash(git branch --show-current)',
].join(',');
const disallowed = [
  'Bash(git diff *--output*)', 'Bash(git log *--output*)', 'Bash(git show *--output*)',
  'Bash(git diff *--ext-diff*)', 'Bash(git log *--ext-diff*)', 'Bash(git show *--ext-diff*)',
].join(',');

function commandKind(input) {
  if (typeof input !== 'string') return 'unknown command';
  const parts = input.trim().split(/\s+/).slice(0, 12);
  const compound = /[;&|`$()<>]/.test(input) ? ' [compound or redirected]' : '';
  if (parts[0] !== 'git') return `other command: ${/^[a-z][a-z-]*$/.test(parts[0]) ? parts[0] : '<masked>'}${compound}`;
  let pos = 1;
  let prefix = 'git';
  if (parts[pos] === '-C') { prefix += ' -C <dir>'; pos += 2; }
  const verb = /^[a-z][a-z-]*$/.test(parts[pos] || '') ? parts[pos] : '<masked>';
  const args = parts.slice(pos + 1).map(arg => {
    if (/^--[a-z][a-z-]*$/.test(arg) || /^-[a-zA-Z]+$/.test(arg)) return arg;
    if (/^(HEAD|main|origin\/HEAD|[0-9a-f]{40})$/.test(arg)) return arg.length === 40 ? '<sha>' : arg;
    return '<arg>';
  });
  return `${prefix} ${verb}${args.length ? ' ' + args.join(' ') : ''}${compound}`;
}

function denialSummary(denials, confirmed = true) {
  if (!Array.isArray(denials) || denials.length === 0) return '';
  const kinds = new Map();
  for (const denial of denials.slice(0, 20)) {
    const kind = denial?.tool_name === 'Bash'
      ? commandKind(denial?.tool_input?.command)
      : String(denial?.tool_name || 'unknown tool').slice(0, 32);
    kinds.set(kind, (kinds.get(kind) || 0) + 1);
  }
  const details = [...kinds].map(([kind, count]) => `${kind} (${count})`).join(', ');
  return confirmed
    ? `Claude denied ${denials.length} tool call(s): ${details}. Permission mode: dontAsk; inspect the Git allowlist in config/bridge-bosun.yaml.`
    : `${denials.length} tool error(s) mentioned permissions: ${details}. Inspect the provider tool result and filesystem access.`;
}

function stderrKind(stderr) {
  if (/unknown (option|argument|flag)|invalid option/i.test(stderr)) return 'CLI_FLAG_ERROR';
  if (/not logged in|unauthorized|api error: 401|invalid_grant/i.test(stderr)) return 'AUTH_ERROR';
  if (/permission denied|EACCES/i.test(stderr)) return 'FILE_PERMISSION_ERROR';
  if (/ENOENT|no such file or directory/i.test(stderr)) return 'FILE_NOT_FOUND';
  return stderr ? 'OTHER_STDERR_ERROR' : '';
}

function createCollector() {
  let finalResult;
  let denials = [];
  let error = '';
  let resultEvent;
  let damagedStream = false;
  const tools = new Map();
  const deniedResults = [];
  return {
    accept(line) {
      if (line.length > 2 * 1024 * 1024) { damagedStream = true; return; }
      let event;
      try { event = JSON.parse(line); } catch { if (line.trim()) damagedStream = true; return; }
      if (!event || typeof event !== 'object') return;
      if (event.type === 'assistant') {
        for (const item of Array.isArray(event.message?.content) ? event.message.content : []) {
          if (item?.type === 'tool_use' && item.id && tools.size < 100) {
            tools.set(item.id, { tool_name: item.name, tool_input: item.input });
          }
        }
      }
      if (event.type === 'user') {
        for (const item of Array.isArray(event.message?.content) ? event.message.content : []) {
          if (item?.type === 'tool_result' && item.is_error && tools.has(item.tool_use_id)
              && /permission|denied|not allowed/i.test(String(item.content).slice(0, 2000))) {
            deniedResults.push(tools.get(item.tool_use_id));
          }
        }
      }
      if (event.type === 'result') {
        if (!zeroedExecutionFailure(event)) resultEvent = event;
        if (typeof event.result === 'string') finalResult = event.result;
        if (Array.isArray(event.permission_denials)) denials = event.permission_denials;
        if (event.is_error) error = String(event.subtype || 'provider error').slice(0, 80);
      }
    },
    output() {
      const incomplete = !finalResult || /^\s*bosun-review:\s*incomplete\s*$/im.test(finalResult);
      const summary = incomplete || error
        ? denialSummary(denials.length ? denials : deniedResults, denials.length > 0)
        : '';
      // Bosun bounds incomplete-review reasons to their first 500 characters.
      return safeReview([summary, finalResult || ''].filter(Boolean).join('\n\n'));
    },
    usage(incomplete = false) { return claudeUsage(resultEvent, incomplete || Boolean(error) || damagedStream); },
    failed() { return Boolean(error || !finalResult); },
    error() { return error; },
  };
}

if (require.main === module) {
  const child = spawn(claude, [
    '--print', '--output-format', 'stream-json', '--verbose',
    '--no-session-persistence', '--permission-mode', 'dontAsk',
    '--tools', 'Read,Glob,Grep,Bash', '--allowedTools', allowed,
    '--disallowedTools', disallowed,
    '--', ...process.argv.slice(2),
  ], { stdio: ['ignore', 'pipe', 'pipe'] });
  const collector = createCollector();
  let stderr = '';
  child.stderr.on('data', chunk => { stderr = (stderr + chunk.toString()).slice(-2048); });
  for (const signal of ['SIGTERM', 'SIGINT']) {
    process.on(signal, () => child.kill(signal));
  }
  const lines = readline.createInterface({ input: child.stdout });
  lines.on('line', line => collector.accept(line));
  let startFailed = false;
  child.on('error', () => { startFailed = true; });
  child.on('close', code => {
    process.stdout.write(usageLine(collector.usage(code !== 0 || collector.failed())));
    if (startFailed) process.stdout.write('Claude executable could not start\n');
    const output = collector.output();
    if (output) process.stdout.write(output + '\n');
    if (collector.failed() || code !== 0) {
      process.stdout.write(`Claude stream failed: ${collector.error() || `exit ${code}`}; stderr category: ${stderrKind(stderr) || 'none'}\n`);
      process.exitCode = 1;
    }
  });
}

module.exports = { commandKind, denialSummary, createCollector, stderrKind };
