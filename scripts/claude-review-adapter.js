// Keep Claude's final answer while surfacing permission failures that its text
// output hides. The provider's raw event stream can contain prompts, source
// code, and credentials, so never forward it to bridgectl or Job logs.
'use strict';

const { spawn } = require('node:child_process');
const readline = require('node:readline');

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

function commandKind(input) {
  if (typeof input !== 'string') return 'unknown command';
  const match = /^\s*git\s+([a-z][a-z-]*)\b/.exec(input);
  return match ? `git ${match[1]}` : 'other command';
}

function denialSummary(denials) {
  if (!Array.isArray(denials) || denials.length === 0) return '';
  const kinds = new Map();
  for (const denial of denials.slice(0, 20)) {
    const kind = denial?.tool_name === 'Bash'
      ? commandKind(denial?.tool_input?.command)
      : String(denial?.tool_name || 'unknown tool').slice(0, 32);
    kinds.set(kind, (kinds.get(kind) || 0) + 1);
  }
  const details = [...kinds].map(([kind, count]) => `${kind} (${count})`).join(', ');
  return `Claude denied ${denials.length} tool call(s): ${details}. Permission mode: dontAsk; inspect the Git allowlist in config/bridge-bosun.yaml.`;
}

function createCollector() {
  let finalResult;
  let denials = [];
  let error = '';
  const tools = new Map();
  const deniedResults = [];
  return {
    accept(line) {
      let event;
      try { event = JSON.parse(line); } catch { return; }
      if (event.type === 'assistant') {
        for (const item of event.message?.content || []) {
          if (item.type === 'tool_use' && item.id && tools.size < 100) {
            tools.set(item.id, { tool_name: item.name, tool_input: item.input });
          }
        }
      }
      if (event.type === 'user') {
        for (const item of event.message?.content || []) {
          if (item.type === 'tool_result' && item.is_error && tools.has(item.tool_use_id)
              && /permission|denied|not allowed/i.test(String(item.content).slice(0, 2000))) {
            deniedResults.push(tools.get(item.tool_use_id));
          }
        }
      }
      if (event.type === 'result') {
        if (typeof event.result === 'string') finalResult = event.result;
        if (Array.isArray(event.permission_denials)) denials = event.permission_denials;
        if (event.is_error) error = String(event.subtype || 'provider error').slice(0, 80);
      }
    },
    output() {
      const summary = denialSummary(denials.length ? denials : deniedResults);
      return [finalResult || '', summary].filter(Boolean).join('\n\n');
    },
    failed() { return Boolean(error || !finalResult); },
    error() { return error; },
  };
}

if (require.main === module) {
  const child = spawn(claude, [
    '--print', '--output-format', 'stream-json', '--verbose',
    '--no-session-persistence', '--permission-mode', 'dontAsk',
    '--tools', 'Read,Glob,Grep,Bash', '--allowedTools', allowed,
    '--', ...process.argv.slice(2),
  ], { stdio: ['ignore', 'pipe', 'ignore'] });
  const collector = createCollector();
  const lines = readline.createInterface({ input: child.stdout });
  lines.on('line', line => {
    if (line.length <= 2 * 1024 * 1024) collector.accept(line);
  });
  child.on('error', () => { process.stdout.write('Claude executable could not start\n'); process.exitCode = 1; });
  child.on('close', code => {
    const output = collector.output();
    if (output) process.stdout.write(output + '\n');
    if (collector.failed() || code !== 0) {
      process.stdout.write(`Claude stream failed: ${collector.error() || `exit ${code}`}\n`);
      process.exitCode = 1;
    }
  });
}

module.exports = { commandKind, denialSummary, createCollector };
