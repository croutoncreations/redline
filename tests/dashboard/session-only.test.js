const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

for (const [file, name, next, field] of [
  ['dashboard.js', 'basePressure', 'function', 'label'],
  ['mobile.js', 'accountStateLine', 'function', 'text'],
]) {
  test(`${file}: session-only seats do not claim a weekly dispatch hold`, () => {
    const source = fs.readFileSync(path.join(__dirname, '../../internal/api/dashboard', file), 'utf8');
    const start = source.indexOf(`function ${name}(`);
    assert.ok(start >= 0, `${name} exists`);
    const end = source.indexOf(`\n${next} `, start + 1);
    const context = vm.createContext({accountState: () => 'ok', accountMismatch: () => false, signedOut: () => false});
    vm.runInContext(source.slice(start, end), context);
    const item = {snapshot: {weekly_unavailable: true}, weekly_policy: 'session_only', latest_decision: {decision: 'WAIT'}};
    assert.equal(context[name](item)[field], 'Weekly not reported · session-only scheduling');
    item.weekly_policy = '';
    assert.equal(context[name](item)[field], 'Dispatch held · weekly not reported');
  });
}
