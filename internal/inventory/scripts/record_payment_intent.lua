-- record_payment_intent: store the PaymentIntent ID on a PAYING Hold, so recovery can retrieve
-- the PaymentIntent by it (ADR-0007).
-- KEYS[1] = the Flash Sale's Holds hash (see reserve.lua)
-- ARGV[1] = the Hold's ID
-- ARGV[2] = the PaymentIntent ID
-- Returns 'OK', or why not: 'NOT_FOUND', or {'WRONG_STATE', <state>}.
local id = ARGV[1]
local state = redis.call('HGET', KEYS[1], id .. ':state')
if not state then
  return 'NOT_FOUND'
end
if state ~= 'PAYING' then
  return {'WRONG_STATE', state}
end
redis.call('HSET', KEYS[1], id .. ':payment_intent', ARGV[2])
return 'OK'
