-- load: put a Flash Sale's stock into Redis when it goes LIVE, once.
-- KEYS[1] = the Flash Sale's counts hash
-- ARGV[1] = units
-- ARGV[2] = the Flash Sale's end time, in Unix milliseconds
-- Returns 1 if it loaded the stock, 0 if the Flash Sale was already loaded (nothing changes).
if redis.call('EXISTS', KEYS[1]) == 1 then
  return 0
end
redis.call('HSET', KEYS[1], 'available', ARGV[1], 'held', 0, 'paying', 0, 'sold', 0, 'ends_at', ARGV[2])
return 1
