-- Preset Outfit Probe: a throwaway test for Preset Wardrobe.
-- Question: can a plugin dress the skater live from a fit's recipe (slot id + item name)?
-- Run it IN THE WORLD (not in a game menu): press F6, or press the button on this plugin's page
-- and close the menu within 10 s. It takes about 25 s; watch the skater. Results are written to
-- out/outfit_probe.txt in the plugin's data folder.

local VERSION = "test 3"
local function now() return reskate.time.now() end
local run_n = (reskate.storage.get("run") or 0) + 1
reskate.storage.set("run", run_n)
local lines, dirty = {}, false
local function log(text)
    table.insert(lines, text)
    dirty = true
end
local function flush()
    if not dirty then return end
    dirty = false
    pcall(reskate.files.write, "out/outfit_probe_" .. run_n .. ".txt", "Preset Outfit Probe " .. VERSION .. "\n" .. table.concat(lines, "\n") .. "\n")
end
local function say(text) pcall(reskate.ui.toast, string.sub(text, 1, 160), "info") end

local function describe(v, depth)
    depth = depth or 0
    if type(v) ~= "table" then return tostring(v) end
    if depth >= 2 then return "{...}" end
    local out, n = {}, 0
    for k, x in v do
        n += 1
        if n > 10 then table.insert(out, "...") break end
        table.insert(out, tostring(k) .. "=" .. describe(x, depth + 1))
    end
    return "{" .. table.concat(out, ", ") .. "}"
end

local function slots_now()
    local ok, list = pcall(reskate.outfit.slots)
    if not ok then return nil, tostring(list) end
    return list
end
local function item_at(list, slot)
    if type(list) ~= "table" then return "?" end
    for _, e in list do
        if tostring(e.slot) == tostring(slot) then return tostring(e.item) end
    end
    return "(slot not listed)"
end

local HEAD = "cust_hats" -- the headwear slot (its number in the game's presets, 2889696005, is a hash of this name)
local job = nil
local steps = {}

local function add(delay_ms, fn) table.insert(steps, {delay = delay_ms, fn = fn}) end

local function build()
    steps = {}
    -- 1. what the game reports (short)
    add(0, function()
        local list = slots_now()
        log("slots(): " .. (list and #list or "none") .. " entries")
        local okw, worn = pcall(reskate.outfit.worn)
        log("worn(): pcall " .. tostring(okw) .. ", " .. describe(worn))
        log("set() was refused for headwear last time: only tattoo, moles, eyewear and hand wear slots. Testing on_top for a whole fit.")
    end)
    -- 2. the helmet on top, for 8 seconds
    local helmet = reskate.outfit.find("HelmetSkateboard_00001")
    add(500, function()
        local ok, res, why = pcall(reskate.outfit.on_top, {helmet})
        log(string.format("HELMET on_top({helmet}) -> pcall %s ok=%s why=%s", tostring(ok), tostring(res), tostring(why)))
        say("HELMET should be on your skater now. Watch for 8 seconds.")
    end)
    add(8000, function()
        local ok, st = pcall(reskate.outfit.on_top_state)
        log("HELMET state: " .. (ok and describe(st) or tostring(st)))
        local list = slots_now()
        log("HELMET slots() cust_hats holds: " .. item_at(list, "cust_hats"))
        pcall(reskate.outfit.on_top, nil)
        say("Helmet taken off.")
    end)
    -- 3. a whole fit on top (items from your own presets), for 15 seconds
    add(3000, function()
        local names, found = {"TshirtTight_00004", "MajiChino", "EMERICA", "Chicago_White_Sox", "Destructo"}, {}
        for _, n in names do
            local f = reskate.outfit.find(n)
            log(string.format("find(%q) = %s", n, tostring(f)))
            if f then table.insert(found, f) end
        end
        local ok, res, why = pcall(reskate.outfit.on_top, found)
        log(string.format("FIT on_top(%d items) -> pcall %s ok=%s why=%s", #found, tostring(ok), tostring(res), tostring(why)))
        say("A WHOLE FIT should be on your skater now (shirt, pants, shoes, hat). Watch for 15 seconds.")
    end)
    add(2000, function()
        local ok, st = pcall(reskate.outfit.on_top_state)
        log("FIT state after 2 s: " .. (ok and describe(st) or tostring(st)))
        local list = slots_now()
        for _, k in {"cust_hats", "cust_tops", "cust_bottoms", "cust_shoes"} do log("FIT slots() " .. k .. " = " .. item_at(list, k)) end
    end)
    add(13000, function()
        pcall(reskate.outfit.on_top, nil)
        say("Fit taken off.")
    end)
    add(1500, function()
        log("done. Everything this probe placed has been cleared; the skater should look as before.")
        say("Outfit probe done.")
    end)
end

local function start()
    if job then return say("Already running.") end
    lines = {}
    build()
    job = {i = 1, at = now()}
    say("Outfit probe running (about 30 s). Watch the skater. Stay in the world.")
end

reskate.on("tick", function()
    if not job then return end
    local s = steps[job.i]
    if not s then job = nil return end
    if now() - job.at >= s.delay then
        local ok, err = pcall(s.fn)
        if not ok then log("error in step " .. job.i .. ": " .. tostring(err)) end
        flush()
        job.i += 1
        job.at = now()
    end
end)

pcall(reskate.input.bind, "outfitprobe", "F6")
reskate.on("action", function(name) if name == "outfitprobe" then start() end end)

local armed = nil
reskate.on("tick", function()
    if armed and now() >= armed then armed = nil start() end
end)

reskate.menu.page("Preset Outfit Probe", function(ui)
    ui:note("Throwaway test. Press F6 in the world, or press the button, close the menu and wait 10 s. Watch the skater for about 25 s.")
    if job then
        ui:note("Running...")
    elseif ui:button("go", "Run in 10 s", true) then
        armed = now() + 10000
        say("Starts in 10 s: close the menu now.")
    end
end)

reskate.on("unload", function()
    pcall(reskate.outfit.clear)
    pcall(reskate.outfit.on_top, nil)
end)
