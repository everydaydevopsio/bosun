// Keep the final Codex review and allowlisted usage. Raw JSON contains source,
// commands and prompts, and must not reach bridgectl or reviewer Job logs.
'use strict';

const { spawn } = require('node:child_process');
const readline = require('node:readline');
const { count, sum, emptyUsage, status, safeReview, usageLine } = require('./provider-usage');
const { stderrKind } = require('./claude-review-adapter');

const codex = '/app/node_modules/@openai/codex/bin/codex.js';
function codexArgs(prompts) {
  // The disposable reviewer Job remains the sandbox; see bridge-bosun.yaml.
  return ['exec', '--json', '--skip-git-repo-check', '--color', 'never', '--sandbox', 'danger-full-access', '--', ...prompts];
}
function createCollector() {
  let finalResult = '';
  let error = '';
  let turns = 0;
  let terminalSeen = false;
  let activeTurn = false;
  let missingTurnUsage = false;
  let damagedStream = false;
  const totals = emptyUsage('codex');
  const counters = ['input_tokens', 'cached_input_tokens', 'output_tokens', 'reasoning_output_tokens'];
  return {
    accept(line) {
      if (line.length > 2 * 1024 * 1024) { damagedStream = true; return; }
      let event;
      try { event = JSON.parse(line); } catch { if (line.trim()) damagedStream = true; return; }
      if (!event || typeof event !== 'object') return;
      if (event.type === 'turn.started') {
        activeTurn = true;
        terminalSeen = false;
      }
      if (event.type === 'item.completed' && event.item?.type === 'agent_message' && typeof event.item.text === 'string') {
        finalResult = event.item.text;
      }
      if (event.type === 'turn.completed' && !terminalSeen) {
        terminalSeen = true;
        activeTurn = false;
        for (const key of counters) {
          const value = count(event.usage?.[key]);
          totals[key] = turns === 0 ? value : sum(totals[key], value);
        }
        if (count(event.usage?.input_tokens) === null || count(event.usage?.output_tokens) === null) missingTurnUsage = true;
        turns++;
      }
      if (event.type === 'turn.failed' || event.type === 'error') {
        error = event.type === 'turn.failed' ? 'TURN_FAILED' : 'PROVIDER_ERROR';
      }
    },
    output() { return safeReview(finalResult); },
    usage(incomplete = false) {
      const usage = { ...totals, turns: turns || null, total_tokens: sum(totals.input_tokens, totals.output_tokens) };
      usage.status = status(usage, incomplete || Boolean(error) || activeTurn || missingTurnUsage || damagedStream);
      return usage;
    },
    failed() { return Boolean(error || !finalResult || !turns || activeTurn); },
    error() { return error; },
  };
}

if (require.main === module) {
  const child = spawn(process.execPath, [codex, ...codexArgs(process.argv.slice(2))], { stdio: ['ignore', 'pipe', 'pipe'] });
  const collector = createCollector();
  let stderr = '';
  child.stderr.on('data', chunk => { stderr = (stderr + chunk.toString()).slice(-2048); });
  for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => child.kill(signal));
  readline.createInterface({ input: child.stdout }).on('line', line => collector.accept(line));
  let startFailed = false;
  child.on('error', () => { startFailed = true; });
  child.on('close', code => {
    process.stdout.write(usageLine(collector.usage(code !== 0 || collector.failed())));
    if (startFailed) process.stdout.write('Codex executable could not start\n');
    const output = collector.output();
    if (output) process.stdout.write(output + '\n');
    if (collector.failed() || code !== 0) {
      process.stdout.write(`Codex stream failed: ${collector.error() || `exit ${code}`}; stderr category: ${stderrKind(stderr) || 'none'}\n`);
      process.exitCode = 1;
    }
  });
}

module.exports = { createCollector, codexArgs };
