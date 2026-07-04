"""Plugin registry. Importing a plugin module self-registers it (see base.register)."""

from . import base  # noqa: F401  (registry surface)
from . import bugfix  # noqa: F401  (self-registers the bugfix job type)
from . import persona_qa  # noqa: F401  (self-registers the persona-qa job type)
