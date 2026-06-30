"""Plugin registry. Importing a plugin module self-registers it (see base.register)."""

from . import base  # noqa: F401  (registry surface)
from . import bugfix  # noqa: F401  (self-registers the bugfix job type)
