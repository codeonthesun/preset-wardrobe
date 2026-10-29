-- A fake ReSkate plugin API, just enough to run PresetWardrobe through its snapping flows.
-- tests/plugin/run.sh puts the plugin between this file and checks.luau.
local T = 0
local handlers = {}
local files = {}
local toasts = {}
local cap = {state = "idle", taken = 0, keptn = 0, saving_until = nil}
local skaters = true
local timers = {}
local page_build
local store = {}

local function json_encode(v)
    local t = type(v)
    if t == "nil" then return "null" end
    if t == "boolean" or t == "number" then return tostring(v) end
    if t == "string" then return string.format("%q", v) end
    if #v > 0 or next(v) == nil then
        local parts = {}
        for _, x in v do table.insert(parts, json_encode(x)) end
        return "[" .. table.concat(parts, ",") .. "]"
    end
    local parts = {}
    for k, x in v do table.insert(parts, string.format("%q:%s", tostring(k), json_encode(x))) end
    return "{" .. table.concat(parts, ",") .. "}"
end

reskate = {
    storage = {get = function(k) return store[k] end, set = function(k, v) store[k] = v end},
    time = {now = function() return T end,
        every = function(ms, fn) table.insert(timers, {at = T + ms, every = ms, fn = fn}) return #timers end,
        after = function(ms, fn) table.insert(timers, {at = T + ms, fn = fn}) return #timers end},
    ui = {toast = function(m, l) table.insert(toasts, (l or "info") .. ": " .. m) return true end},
    menu = {game = function() return {open = skaters, skaters = skaters, buttons = {}} end,
        page = function(title, fn) page_build = fn end},
    files = {write = function(p, t) files[p] = t return true end, read = function(p) return files[p] end,
        reveal = function() return true end},
    json = {encode = json_encode, decode = function(t) return {"Own_Hoodie1", "Own_pants6"} end},
    outfit = {worn = function() return {{item = "Own_Hoodie1"}} end, board = function() return {{item = "Own_Deck"}} end,
        title = function(k) return "Title of " .. k end},
    capture = {
        start = function(o) cap.state = "recording" cap.taken = 0 cap.keptn = 0 cap.skip = o.skip return true end,
        state = function() return {state = cap.state, taken = cap.taken, kept = cap.keptn, depth = true} end,
        kept = function() local k = {} for i = 1, cap.keptn do table.insert(k, i) end return k end,
        save = function(p, o) assert(#o.frames == 1) cap.state = "saving" cap.saving_until = T + 300 cap.path = p return true end,
        discard = function() cap.state = "idle" end,
    },
    input = {bind = function() return true end},
    log = function(m) print("LOG", m) end,
    on = function(ev, fn) handlers[ev] = fn end,
}
vector = {create = function(x, y, z) return {x = x, y = y, z = z} end}

local fakeD = {
    size = function() return {x = 2560, y = 1440} end,
    measure = function(_, text, s) return {x = #text * 10, y = 20} end,
    rect = function() end, text = function() end, circle = function() end,
}
fakeD.size = function(self) return {x = 2560, y = 1440} end

local fakeUI = setmetatable({}, {__index = function(_, name)
    return function(self, ...)
        local args = {...}
        if name == "slider" then return args[3] end
        if name == "toggle" then return args[3] end
        if name == "button" then return false end
        return nil
    end
end})


-- the plugin goes here
