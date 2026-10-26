const {test}=require('node:test');
const assert=require('node:assert/strict');
const {duration}=require('../frontend/statistics.js');
test('connected time has no fabricated unknown value and preserves days',()=>{
  for(const value of [null,undefined,NaN,Infinity,-1,'10']) assert.equal(duration(value),'');
  assert.equal(duration(0),'00:00:00');
  assert.equal(duration(3661.9),'01:01:01');
  assert.equal(duration(90061),'1 д. 01:01:01');
});
