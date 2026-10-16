// A sample AI app with Witnessmark evidence in one line (used by the end-to-end test).
//
// Run an agent first (`witnessmark-agent connect <token>`), then:
//   npm install witnessmark     # here it is imported from this repository
//   node examples/node-app/app.mjs
//
// With openai, @anthropic-ai/sdk or @aws-sdk/client-bedrock-runtime installed,
// witnessmark.init() records their calls by itself; this sample has no provider
// keys, so it reports each call with llmCall.
import * as witnessmark from '../../sdk/js/lib/index.mjs';

witnessmark.init({ service: 'referral-api' }); // the one line; the agent URL comes from WITNESSMARK_AGENT_URL

const calls = Number(process.env.SAMPLE_CALLS ?? 20);

async function summarise(n) {
  return witnessmark.trace(() =>
    witnessmark.prompt('referral-summary', '7', () =>
      witnessmark.personalData({ redacted: true }, () =>
        witnessmark.guardrail(async (g) => {
          await witnessmark.llmCall({ provider: 'anthropic', model: 'claude-sonnet-4-5', region: 'us-east-1' }, async (c) => {
            c.usage(2000 + n, 150);
          });
          g.allow();
        }),
      ),
    ),
  );
}

for (let i = 0; i < calls; i++) await summarise(i);
if (!(await witnessmark.flush(10000))) throw new Error('the agent did not take the spans');
console.log('node sample sent', JSON.stringify(witnessmark.stats()));
await witnessmark.shutdown();
