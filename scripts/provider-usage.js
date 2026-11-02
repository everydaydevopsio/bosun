// Shared allowlisted telemetry helpers. Never serialize provider events directly.
'use strict';

const prefix = 'BOSUN_USAGE_V1 ';
const fields = [
  'input_tokens', 'cached_input_tokens', 'cache_write_input_tokens',
  'cache_write_30m_tokens', 'cache_write_12h_tokens', 'output_tokens',
  'reasoning_output_tokens', 'total_tokens', 'turns', 'provider_reported_cost_usd',
];
function count(value) { return Number.isSafeInteger(value) && value >= 0 ? value : null; }
function cost(value) { return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null; }
function sum(...values) {
  return values.every(value => value !== null) ? count(values.reduce((a, b) => a + b, 0)) : null;
}
function modelName(value) {
  return typeof value === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9._:/[\]-]{0,127}$/.test(value) ? value : null;
}
function costBasis(value) {
  if (value === undefined) return null; // Older CLIs did not report a basis.
  return value === 'list' || value === 'managed' ? value : 'unknown';
}
function emptyUsage(source) {
  return { source, status: 'missing', model: null, ...Object.fromEntries(fields.map(key => [key, null])) };
}
function status(usage, incomplete) {
  const measured = fields.some(key => key !== 'turns' && usage[key] !== null)
    || usage.model_usage?.some(model => fields.some(key => key !== 'turns' && model[key] !== null));
  if (!measured) return 'missing';
  return incomplete || usage.input_tokens === null || usage.output_tokens === null ? 'partial' : 'available';
}
function safeReview(text) {
  // Review text is model-authored, so it must not impersonate the adapter's
  // control line when the worker extracts numeric telemetry from stdout.
  return text.replace(/^BOSUN_USAGE_V1 /gm, 'BOSUN_USAGE_TEXT_V1 ');
}
function usageLine(usage) { return prefix + JSON.stringify(usage) + '\n'; }

function claudeUsage(event, incomplete) {
  const normalized = emptyUsage('claude');
  const usage = event?.usage || {};
  const input = count(usage.input_tokens);
  normalized.cached_input_tokens = count(usage.cache_read_input_tokens);
  normalized.cache_write_input_tokens = count(usage.cache_creation_input_tokens);
  normalized.input_tokens = sum(input, normalized.cached_input_tokens, normalized.cache_write_input_tokens);
  normalized.output_tokens = count(usage.output_tokens);
  normalized.total_tokens = sum(normalized.input_tokens, normalized.output_tokens);
  normalized.turns = count(event?.num_turns);
  normalized.provider_reported_cost_usd = cost(event?.total_cost_usd);
  let completeModels = false;
  if (event?.modelUsage && typeof event.modelUsage === 'object' && !Array.isArray(event.modelUsage)) {
    normalized.model_usage = Object.entries(event.modelUsage).slice(0, 32).flatMap(([name, value]) => {
      const model = modelName(name);
      if (!model || !value || typeof value !== 'object') return [];
      const basis = costBasis(value.costBasis);
      const read = count(value.cacheReadInputTokens);
      const write = count(value.cacheCreationInputTokens);
      const totalInput = sum(count(value.inputTokens), read, write);
      const output = count(value.outputTokens);
      return [{ model, input_tokens: totalInput, cached_input_tokens: read, cache_write_input_tokens: write,
        cache_write_30m_tokens: null, cache_write_12h_tokens: null, output_tokens: output,
        reasoning_output_tokens: null, total_tokens: sum(totalInput, output), turns: null,
        cost_basis: basis, provider_reported_cost_usd: basis === 'unknown' ? null : cost(value.costUSD) }];
    });
    if (Object.values(event.modelUsage).some(value => value && typeof value === 'object' && costBasis(value.costBasis) === 'unknown')) {
      // An unknown price table may produce zero USD despite consumed tokens.
      // Do not expose an apparently complete total from a partially priced run.
      normalized.provider_reported_cost_usd = null;
    }
    if (normalized.model_usage.length === 1) normalized.model = normalized.model_usage[0].model;
    completeModels = normalized.model_usage.length > 0 && normalized.model_usage.length === Object.keys(event.modelUsage).length;
    if (completeModels) {
      // modelUsage includes helper and subagent calls. The result's usage
      // covers only the main loop, so it must not override whole-tree totals.
      for (const key of ['input_tokens', 'cached_input_tokens', 'cache_write_input_tokens', 'output_tokens', 'total_tokens']) {
        normalized[key] = sum(...normalized.model_usage.map(model => model[key]));
      }
    }

  }
  normalized.status = status(normalized, incomplete || !completeModels);
  return normalized;
}

function zeroedExecutionFailure(event) {
  if (event?.subtype !== 'error_during_execution') return false;
  // Claude can replace cumulative totals with zeros after a session crash.
  // Preserve an earlier trustworthy aggregate instead of calling that free.
  const usage = claudeUsage(event, true);
  const hasMeasuredWork = sample => fields.some(key => key !== 'turns' && sample[key] > 0);
  return !hasMeasuredWork(usage) && !usage.model_usage?.some(hasMeasuredWork);
}

module.exports = { count, sum, emptyUsage, status, safeReview, usageLine, claudeUsage, zeroedExecutionFailure };
