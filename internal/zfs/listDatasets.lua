-- listDatasets returns the direct children of a dataset and the snapshots of
-- those children, each with its zdap:* user properties, keyed by full dataset
-- path.
--
-- It replaces the recursive open that zdapd used to do through libzfs. That
-- opened a handle per dataset and loaded all 91 native properties on each one
-- to read at most 8; this reads only the zdap:* properties, in one kernel call.
-- zdap keeps bases, clones and snapshots as direct children of the pool
-- dataset, so one level of children is enough.
--
--   argv[1]  dataset path to list below, e.g. zdap/1/databases
--
-- Properties are read with zfs.list.user_properties rather than zfs.get_prop
-- for two reasons: it is one pass instead of one lookup per property, and
-- get_prop throws a Lua error for a property that is not set. Channel programs
-- have pcall disabled, so a throw cannot be caught and would abort the whole
-- program. Clones legitimately lack zdap:healthy and zdap:expires_at until
-- something sets them.
--
-- The result is a map rather than an array because a channel program returns an
-- nvlist, which is keyed by strings.

args = ...
argv = args["argv"]

root = argv[1]
results = {}

-- zdapProps collects the zdap:* user properties of one dataset. Other user
-- properties are skipped so that the result stays bounded by what zdap uses.
function zdapProps(ds)
  local props = {}
  for name, value, source in zfs.list.user_properties(ds) do
    if string.sub(name, 1, 5) == "zdap:" then
      props[name] = value
    end
  end
  return props
end

function add(ds)
  results[ds] = {name = ds, props = zdapProps(ds)}
end

for child in zfs.list.children(root) do
  add(child)
  for snap in zfs.list.snapshots(child) do
    add(snap)
  end
end

return results
