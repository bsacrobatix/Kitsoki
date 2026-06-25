// Hidden oracle for bug5 (fix 8737281d): membership type codes must NOT require the RG prefix.
use cyberware_resource_group::domain::error::DomainError;
use cyberware_resource_group::domain::validation::{self, RG_TYPE_PREFIX};

#[test]
fn membership_accepts_non_rg_prefix() {
    assert!(validation::validate_membership_type_code("gts.cf.core.idp.user.v1~").is_ok());
}
#[test]
fn membership_accepts_rg_prefixed() {
    let code = format!("{RG_TYPE_PREFIX}y.core.tn.tenant.v1~");
    assert!(validation::validate_membership_type_code(&code).is_ok());
}
#[test]
fn membership_rejects_empty() {
    assert!(matches!(validation::validate_membership_type_code("").unwrap_err(), DomainError::Validation { .. }));
}
#[test]
fn membership_rejects_too_long() {
    let long_code = format!("gts.cf.core.idp.user.v1~{}", "a".repeat(1100));
    assert!(matches!(validation::validate_membership_type_code(&long_code).unwrap_err(), DomainError::Validation { .. }));
}
