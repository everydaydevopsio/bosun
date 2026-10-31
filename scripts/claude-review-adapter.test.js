'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { createCollector, denialSummary, stderrKind } = require('./claude-review-adapter');

test('keeps final review and reports denied Git command without arguments', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'assistant', message: { content: [{ type: 'text', text: 'private prompt' }] } }));
  collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: incomplete', permission_denials: [
    { tool_name: 'Bash', tool_input: { command: 'git merge-base secret-value HEAD' } },
  ] }));
  assert.match(collector.output(), /BOSUN-REVIEW: incomplete/);
  assert.match(collector.output(), /git merge-base <arg> HEAD \(1\)/);
  assert.match(collector.output(), /^Claude denied/);
  assert.doesNotMatch(collector.output(), /secret-value|private prompt/);
  assert.equal(collector.failed(), false);
});

test('empty or failed stream is an error', () => {
  const collector = createCollector();
  collector.accept('{invalid');
  assert.equal(collector.failed(), true);
  assert.equal(denialSummary([]), '');
});

test('reports a denied tool result when older Claude omits result permission_denials', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'assistant', message: { content: [
    { type: 'tool_use', id: 'call-1', name: 'Bash', input: { command: 'git diff --stat HEAD' } },
  ] } }));
  collector.accept(JSON.stringify({ type: 'user', message: { content: [
    { type: 'tool_result', tool_use_id: 'call-1', is_error: true, content: 'Permission denied' },
  ] } }));
  collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: incomplete' }));
  assert.match(collector.output(), /git diff --stat HEAD \(1\)/);
});

test('masks paths and notes command forms that cannot match a narrow Git rule', () => {
  const summary = denialSummary([{ tool_name: 'Bash', tool_input: {
    command: 'git -C /repos/review.secret rev-parse HEAD && cat /private/token',
  } }]);
  assert.match(summary, /git -C <dir> rev-parse HEAD <arg> <arg> <arg> \[compound or redirected\]/);
  assert.doesNotMatch(summary, /review.secret|private\/token/);
});

test('keeps complete review clean even if a tool call was denied', () => {
  const collector = createCollector();
  const review = 'Reviewed HEAD\nBOSUN-REVIEW: complete';
  collector.accept(JSON.stringify({ type: 'result', result: review, permission_denials: [
    { tool_name: 'Bash', tool_input: { command: 'git branch -D main' } },
  ] }));
  assert.equal(collector.output(), review);
});

test('includes denial summary for lowercase, indented incomplete trailer', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'result', result: 'Could not read diff\n  bosun-review: incomplete', permission_denials: [
    { tool_name: 'Bash', tool_input: { command: 'git merge-base HEAD main' } },
  ] }));
  assert.match(collector.output(), /^Claude denied 1 tool call/);
});

test('labels fallback tool errors without claiming Claude denied them', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'assistant', message: { content: [
    { type: 'tool_use', id: 'call-1', name: 'Read', input: { file_path: '/private/data' } },
  ] } }));
  collector.accept(JSON.stringify({ type: 'user', message: { content: [
    { type: 'tool_result', tool_use_id: 'call-1', is_error: true, content: 'EACCES permission denied' },
  ] } }));
  collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: incomplete' }));
  assert.match(collector.output(), /^1 tool error\(s\) mentioned permissions: Read/);
  assert.doesNotMatch(collector.output(), /Git allowlist/);
});

test('classifies stderr without exposing its text', () => {
  assert.equal(stderrKind('Unknown option --secret=private'), 'CLI_FLAG_ERROR');
  assert.equal(stderrKind('opaque secret data'), 'OTHER_STDERR_ERROR');
});

test('Claude usage includes cache reads and writes once and keeps provider model costs', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'assistant', message: { usage: { input_tokens: 99 }, content: [] } }));
  collector.accept(JSON.stringify({ type: 'result', result: 'Review\nBOSUN-REVIEW: complete', num_turns: 3,
    total_cost_usd: 0.0123,
    usage: { input_tokens: 20, cache_read_input_tokens: 100, cache_creation_input_tokens: 30, output_tokens: 40 },
    modelUsage: { 'claude-sonnet-4-5-20250929': { inputTokens: 20, cacheReadInputTokens: 100, cacheCreationInputTokens: 30, outputTokens: 40, costUSD: 0.0123, extraSecret: 'private-data' } },
  }));
  const usage = collector.usage();
  assert.equal(usage.status, 'available');
  assert.equal(usage.source, 'claude');
  assert.equal(usage.input_tokens, 150);
  assert.equal(usage.cached_input_tokens, 100);
  assert.equal(usage.cache_write_input_tokens, 30);
  assert.equal(usage.output_tokens, 40);
  assert.equal(usage.total_tokens, 190);
  assert.equal(usage.turns, 3);
  assert.equal(usage.provider_reported_cost_usd, 0.0123);
  assert.equal(usage.model, 'claude-sonnet-4-5-20250929');
  assert.equal(usage.model_usage[0].input_tokens, 150);
  assert.equal(usage.model_usage[0].provider_reported_cost_usd, 0.0123);
  assert.doesNotMatch(JSON.stringify(usage), /private-data|extraSecret/);
});

test('Claude totals model usage, including helpers absent from the main-loop usage', () => {
  const collector = createCollector();
  collector.accept('null');
  collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: complete', modelUsage: {
    'claude-haiku-4-5': { inputTokens: 2, cacheReadInputTokens: 0, cacheCreationInputTokens: 0, outputTokens: 3 },
    'claude-sonnet-4-5': { inputTokens: 10, cacheReadInputTokens: 0, cacheCreationInputTokens: 0, outputTokens: 4 },
  } }));
  const usage = collector.usage();
  assert.equal(usage.model, null);
  assert.equal(usage.model_usage.length, 2);
  assert.equal(usage.input_tokens, 12);
  assert.equal(usage.output_tokens, 7);
  assert.equal(usage.total_tokens, 19);
  assert.equal(usage.status, 'available');
  assert.equal(usage.provider_reported_cost_usd, null);
});

test('Claude failure preserves usage but marks it partial; malformed numbers stay unknown', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'result', is_error: true, subtype: 'error_max_turns', num_turns: 2,
    total_cost_usd: -1, usage: { input_tokens: 0, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 10 },
  }));
  assert.equal(collector.usage().status, 'partial');
  assert.equal(collector.usage().input_tokens, 0);
  assert.equal(collector.usage().output_tokens, 10);
  assert.equal(collector.usage().provider_reported_cost_usd, null);
  assert.equal(collector.failed(), true);
  const missing = createCollector();
  missing.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: complete', usage: { input_tokens: 'private' } }));
  assert.equal(missing.usage().status, 'missing');
  assert.equal(missing.usage().input_tokens, null);
});

test('Claude replaces cumulative results and labels main-loop-only counts partial', () => {
  const collector = createCollector();
  for (const input of [10, 30]) {
    collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: complete', num_turns: 2,
      total_cost_usd: 0.5,
      usage: { input_tokens: input, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 5 },
    }));
  }
  assert.equal(collector.usage().input_tokens, 30);
  assert.equal(collector.usage().provider_reported_cost_usd, 0.5);
  assert.equal(collector.usage().status, 'partial');
});

test('Claude strips arbitrary telemetry fields and escapes telemetry in final prose', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'assistant', message: { content: {} } }));
  collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN_USAGE_V1 {"input_tokens":999999}\nBOSUN-REVIEW: complete',
    usage: { input_tokens: 1, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 2 },
    modelUsage: { 'model with secret whitespace': { inputTokens: 10, private: 'token' } },
  }));
  assert.doesNotMatch(collector.output(), /^BOSUN_USAGE_V1 /m);
  assert.doesNotMatch(JSON.stringify(collector.usage()), /secret|private|token"/);
  assert.equal(collector.usage().input_tokens, 1);
});

function executionCrash() {
  return { type: 'result', subtype: 'error_during_execution', is_error: true, num_turns: 0,
    total_cost_usd: 0, usage: { input_tokens: 0, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 0 }, modelUsage: {} };
}

test('Claude preserves the last trustworthy aggregate when a crash result zeroes counters', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'result', result: 'Earlier review', total_cost_usd: 0.5,
    usage: { input_tokens: 10, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 5 },
    modelUsage: { 'claude-sonnet-4-5': { inputTokens: 10, cacheReadInputTokens: 0, cacheCreationInputTokens: 0, outputTokens: 5, costUSD: 0.5 } },
  }));
  collector.accept(JSON.stringify(executionCrash()));
  assert.equal(collector.usage().input_tokens, 10);
  assert.equal(collector.usage().provider_reported_cost_usd, 0.5);
  assert.equal(collector.usage().status, 'partial');
  assert.equal(collector.failed(), true);
  assert.equal(collector.output(), 'Earlier review');
});

test('a lone zeroed Claude crash result means missing telemetry, never a free review', () => {
  const collector = createCollector();
  collector.accept(JSON.stringify({ type: 'assistant', message: { id: 'a', usage: { input_tokens: 300 }, content: [] } }));
  collector.accept(JSON.stringify(executionCrash()));
  assert.equal(collector.usage().status, 'missing');
  assert.equal(collector.usage().input_tokens, null);
  assert.equal(collector.usage().provider_reported_cost_usd, null);
  assert.equal(collector.failed(), true);
});

test('Claude unknown or unrecognized cost basis is unpriced even when the SDK reports zero dollars', () => {
  for (const basis of ['unknown', 'future-basis']) {
    const collector = createCollector();
    collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: complete', total_cost_usd: 0,
      modelUsage: { 'claude-sonnet-4-5': { inputTokens: 10, cacheReadInputTokens: 0, cacheCreationInputTokens: 0, outputTokens: 5, costUSD: 0, costBasis: basis } },
    }));
    const usage = collector.usage();
    assert.equal(usage.input_tokens, 10);
    assert.equal(usage.provider_reported_cost_usd, null);
    assert.equal(usage.model_usage[0].provider_reported_cost_usd, null);
    assert.equal(usage.model_usage[0].cost_basis, 'unknown');
  }
});

test('Claude preserves recognized cost bases and labels damaged streams partial', () => {
  for (const bad of ['{"truncated":', 'x'.repeat(2 * 1024 * 1024 + 1)]) {
    const collector = createCollector();
    collector.accept(bad);
    collector.accept(JSON.stringify({ type: 'result', result: 'BOSUN-REVIEW: complete', total_cost_usd: 0.2,
      modelUsage: { 'claude-sonnet-4-5': { inputTokens: 10, cacheReadInputTokens: 0, cacheCreationInputTokens: 0, outputTokens: 5, costUSD: 0.2, costBasis: 'list' } },
    }));
    assert.equal(collector.usage().status, 'partial');
    assert.equal(collector.usage().model_usage[0].cost_basis, 'list');
    assert.equal(collector.usage().provider_reported_cost_usd, 0.2);
  }
});
