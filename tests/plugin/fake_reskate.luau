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
local turns = {}
saved_frames = {} -- path -> how many frames were saved in it
-- The Skaters page preview: left turned 70 degrees by an earlier snap, and the stick turns it the
-- other way from the plugin's first guess, so it has to learn the direction.
preview = {angle = math.rad(70), stick = 0, held_until = 0, sign = -1, speed = 3.0, seq = 0}
function plant(ms)
    if T < preview.held_until then
        preview.angle += preview.stick * preview.sign * preview.speed * ms / 1000
        preview.angle = (preview.angle + math.pi) % (2 * math.pi) - math.pi
    end
    preview.seq += 1
end
slider_override = {} -- id -> value the "player" sets on the plugin's page
cards_seen = {} -- card titles drawn on the page
tab_override = nil -- the tab the "player" has open
last_tabs = nil
press = {} -- id -> true: the "player" presses that button on the next build

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
        remove = function(p) files[p] = nil return true end,
        reveal = function() return true end},
    json = {encode = json_encode, decode = function(t) return {"Own_Hoodie1", "Own_pants6"} end},
    outfit = {worn = function() return {{item = "Own_Hoodie1"}} end, board = function() return {{item = "Own_Deck"}} end,
        title = function(k) return "Title of " .. k end},
    capture = {
        start = function(o) cap.state = "recording" cap.taken = 0 cap.keptn = 0 cap.skip = o.skip return true end,
        state = function() return {state = cap.state, taken = cap.taken, kept = cap.keptn, depth = true,
            file = if cap.state == "saved" then cap.path else nil} end,
        kept = function() local k = {} for i = 1, cap.keptn do table.insert(k, i) end return k end,
        save = function(p, o)
            assert(o.format == "apng" or #o.frames == 1, "a png is one frame")
            for _, f in o.frames do assert(f >= 1 and f <= cap.keptn, "save asks for kept frames") end
            saved_frames[p] = #o.frames
            cap.state = "saving" cap.saving_until = T + 300 cap.path = p return true
        end,
        discard = function() cap.state = "idle" end,
    },
    input = {bind = function() return true end},
    room = {
        turn = function(v)
            table.insert(turns, v)
            preview.stick, preview.held_until = v, T + 300
            return true
        end,
        pose = function(subject)
            -- A turn about the vertical whose heading (as the plugin reads it) is preview.angle.
            local th = preview.angle - math.pi / 2
            return {rotation = {0, math.sin(th / 2), 0, math.cos(th / 2)}, sequence = preview.seq}
        end,
    },
    log = function(m) print("LOG", m) end,
    -- A plugin can listen to an event more than once: each listener is called, in the order added.
    on = function(ev, fn)
        local prev = handlers[ev]
        handlers[ev] = if prev then function(...) prev(...) return fn(...) end else fn
    end,
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
        if name == "slider" then
            if slider_override[args[1]] ~= nil then return slider_override[args[1]] end
            return args[3]
        end
        if name == "toggle" then return args[3] end
        if name == "card" then cards_seen[args[1]] = true end
        if name == "tabs" then
            last_tabs = args[2]
            if tab_override then return tab_override end
            return args[3]
        end
        if name == "button" then
            if press[args[1]] then press[args[1]] = nil return true end
            return false
        end
        return nil
    end
end})


-- the plugin goes here
