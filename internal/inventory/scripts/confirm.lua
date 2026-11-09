-- confirm: move a PAYING Hold to SOLD once its Order is written (ADR-0006). Confirming a SOLD
-- Hold again changes nothing, since the confirm response and the payment webhook may both
-- confirm the same Hold.
-- KEYS[1] = the Flash Sale's counts hash
-- KEYS[2] = the Flash Sale's Holds hash (see reserve.lua)
-- ARGV[1] = the Hold's ID
-- Returns 'OK' (sold now or already), or why not: 'NOT_FOUND', or {'NOT_PAYING', <state>}.
local id = ARGV[1]
local state = redis.call('HGET', KEYS[2], id .. ':state')
if not state then
  return 'NOT_FOUND'
end
if state == 'SOLD' then
  return 'OK'
end
if state ~= 'PAYING' then
  return {'NOT_PAYING', state}
end
redis.call('HSET', KEYS[2], id .. ':state', 'SOLD')
redis.call('HINCRBY', KEYS[1], 'paying', -1)
redis.call('HINCRBY', KEYS[1], 'sold', 1)
return 'OK'
