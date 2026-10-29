local function advance(ms)
    local stepms = 16
    local t_end = T + ms
    while T < t_end do
        T += stepms
        if cap.state == "recording" then
            cap.taken += 1
            if cap.taken > (cap.skip or 0) then cap.keptn += 1 end
        end
        if cap.state == "saving" and T >= cap.saving_until then
            cap.state = "saved"
            files[cap.path] = "<png>"
        end
        for _, tm in timers do
            if tm.at and T >= tm.at then
                tm.fn()
                tm.at = if tm.every then T + tm.every else nil
            end
        end
        handlers.tick()
        handlers.draw(fakeD)
    end
end


local failures = 0
local function check(ok, what)
    if ok then print("ok   " .. what) else print("FAIL " .. what); failures += 1 end
end
local function snaps()
    local n = 0
    for p in files do if p:match("^snaps/fit%-%d+%.png$") then n += 1 end end
    return n
end

-- One fit with F8.
page_build(fakeUI)
handlers.action("presetwardrobe_snap")
advance(8000)
check(snaps() == 1, "F8 snaps one fit")
check((files["snaps/index.json"] or ""):find('"seq":1') ~= nil, "the snap is listed in snaps/index.json")

-- Leaving the Skaters page pauses a snap, coming back finishes it.
handlers.action("presetwardrobe_snap")
skaters = false
advance(3000)
skaters = true
advance(8000)
check(snaps() == 2, "a paused snap finishes on return")

-- Messages from Preset Wardrobe are shown once each.
files["added.json"] = "ack"
reskate.json.decode = function(t)
    if t == "ack" then return {version = 2, messages = {{id = 1, message = "Added A"}, {id = 2, message = "Added B"}}} end
    return {}
end
advance(4000)
local shown = 0
for _, t in toasts do if t:find("Added [AB]") then shown += 1 end end
check(shown == 2, "both catalogue messages shown, once each")

if failures > 0 then error(failures .. " plugin check(s) failed") end
print("all plugin checks passed")
